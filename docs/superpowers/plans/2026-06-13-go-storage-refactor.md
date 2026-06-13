# Plan #1: Go Storage Interface Refactor + R2 Backend

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the path-based `Storage` interface with a key-based one that supports presigned URLs. Implement `R2Storage` for production. Add a `/internal/blob` handler so `LocalDiskStorage` can present the same URL-handoff protocol locally. Wire `STORAGE_BACKEND` env var to select impl.

**Architecture:** `Storage` becomes a small interface around opaque keys (`Key`, `Has`, `SignGet`, `SignPut`, `Commit`, `Open`). Two implementations: `LocalDiskStorage` (existing, refactored) returns URLs to a Go-side `/internal/blob/{key}` handler protected by HMAC tokens; `R2Storage` (new) returns presigned R2 URLs via the AWS SDK. The processor client stays path-based in this plan — handlers that call Python continue to use `LocalDiskStorage`-only filesystem paths via a transitional helper. The full URL-handoff to Python is plan #4.

**Tech Stack:** Go 1.22, chi router, AWS SDK for Go v2 (`github.com/aws/aws-sdk-go-v2/service/s3` against R2 endpoint), existing `services.Signer` for blob tokens.

**Out of scope for this plan:** Python service split, processor URL handoff, sig payload change to `videoID|sessionID|issuedAt`, Turnstile/WAF, Dockerfile.

---

## Reference: Target Storage interface

```go
// backend/services/storage.go

type Storage interface {
    Key(videoID, name string) string
    Has(ctx context.Context, key string) (bool, error)
    SignGet(ctx context.Context, key string) (url string, err error)
    SignPut(ctx context.Context, key string) (url string, err error)
    Commit(ctx context.Context, key string) error
    Open(ctx context.Context, key string) (io.ReadCloser, error)
}
```

`Key()` is a pure function (no I/O, no error). All other methods take an opaque key produced by `Key()`. No caller may construct keys directly.

## File structure

**Modify:**
- `backend/services/storage.go` — refactor `Storage` interface and `LocalDiskStorage`.
- `backend/services/storage_test.go` — switch tests to key-based API; add Key/SignGet/SignPut tests.
- `backend/config/config.go` — add `StorageBackend`, `R2*`, `BlobBaseURL` fields.
- `backend/config/config_test.go` — tests for new fields.
- `backend/cmd/server/main.go` — select Storage impl via `cfg.StorageBackend`.
- `backend/api/router.go` — mount `/internal/blob/{key}` conditionally.
- `backend/api/handlers/audio.go`, `melody.go`, `preview_audio.go`, `preview_melody.go`, `preview_stems.go`, `preview_shift.go`, `preview_key.go`, `preview.go`, `generate.go` — switch to key-based API; replace `os.Open(LocalPath(...))` with `storage.Open(ctx, key)`.
- All `*_test.go` for the above handlers — use new Storage API in fakes.

**Create:**
- `backend/services/storage_r2.go` — `R2Storage` impl.
- `backend/services/storage_r2_test.go` — unit tests via mocked HTTP.
- `backend/services/storage_blob_token.go` — HMAC token helper for `/internal/blob` (sign and verify `key|exp`).
- `backend/services/storage_blob_token_test.go` — tests.
- `backend/api/handlers/blob.go` — GET/PUT `/internal/blob/{key}` for local dev.
- `backend/api/handlers/blob_test.go` — tests.

**Transitional (will be removed in plan #4):**
- `LocalDiskStorage` keeps a non-interface method `FilesystemPathForLocalProcessor(key) (string, error)`. Used only by code paths that call the path-based `ProcessorClient`. R2 mode will fail loudly if these paths are hit until plan #4 ships URL-based processor calls.

---

## Task 1: Refactor `Storage` interface to key-based API (LocalDiskStorage only)

**Goal:** Switch `Storage` to take opaque keys. Update `LocalDiskStorage` accordingly. Keep `SignGet`/`SignPut` returning a placeholder `""` for now — wired in Task 4. No external callers updated yet.

**Files:**
- Modify: `backend/services/storage.go`
- Modify: `backend/services/storage_test.go`

- [ ] **Step 1: Write failing tests for the new key-based API**

Replace the existing key-based test bodies (currently `(videoID, name)`) with the new shape. Add a test for `Key()`:

```go
// backend/services/storage_test.go (additions/replacements)

func TestLocalDiskStorage_Key_isPureFunction(t *testing.T) {
    s, err := NewLocalDiskStorage(t.TempDir())
    require.NoError(t, err)

    cases := []struct {
        name        string
        videoID     string
        objectName  string
        wantSuffix  string
    }{
        {"top-level file", "abc12345678", "melody.json", "abc12345678/melody.json"},
        {"nested path", "abc12345678", "shifted/0/audio.mp3", "abc12345678/shifted/0/audio.mp3"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := s.Key(tc.videoID, tc.objectName)
            require.Equal(t, tc.wantSuffix, got)
        })
    }
}

func TestLocalDiskStorage_HasOpenCommit_byKey(t *testing.T) {
    s, err := NewLocalDiskStorage(t.TempDir())
    require.NoError(t, err)
    ctx := context.Background()

    key := s.Key("abc12345678", "melody.json")

    has, err := s.Has(ctx, key)
    require.NoError(t, err)
    require.False(t, has)

    // Stage a non-empty source file and Commit by key.
    src := filepath.Join(t.TempDir(), "src.json")
    require.NoError(t, os.WriteFile(src, []byte(`{"ok":true}`), 0o644))
    require.NoError(t, s.Commit(ctx, key, src))

    has, err = s.Has(ctx, key)
    require.NoError(t, err)
    require.True(t, has)

    rc, err := s.Open(ctx, key)
    require.NoError(t, err)
    defer rc.Close()
    body, err := io.ReadAll(rc)
    require.NoError(t, err)
    require.Equal(t, `{"ok":true}`, string(body))
}
```

(Delete or rewrite the equivalent old `(videoID, name)` tests — same coverage, new signature.)

- [ ] **Step 2: Run tests, expect compile failures**

```
cd backend && go test ./services/ -run TestLocalDiskStorage -v
```

Expected: build error (`Key` undefined; `Has`/`Open`/`Commit` signatures mismatch).

- [ ] **Step 3: Refactor the interface and `LocalDiskStorage`**

```go
// backend/services/storage.go

type Storage interface {
    Key(videoID, name string) string
    Has(ctx context.Context, key string) (bool, error)
    SignGet(ctx context.Context, key string) (string, error)
    SignPut(ctx context.Context, key string) (string, error)
    Commit(ctx context.Context, key, localPath string) error
    Open(ctx context.Context, key string) (io.ReadCloser, error)
}

type LocalDiskStorage struct {
    root string
}

func (s *LocalDiskStorage) Key(videoID, name string) string {
    return path.Join(videoID, name) // forward slashes, opaque to callers
}

func (s *LocalDiskStorage) absPath(key string) string {
    return filepath.Join(s.root, filepath.FromSlash(key))
}

func (s *LocalDiskStorage) Has(_ context.Context, key string) (bool, error) {
    info, err := os.Stat(s.absPath(key))
    if err != nil {
        if os.IsNotExist(err) {
            return false, nil
        }
        return false, err
    }
    return info.Size() > 0, nil
}

func (s *LocalDiskStorage) Commit(_ context.Context, key, localPath string) error {
    target := s.absPath(key)
    if localPath == target {
        return nil
    }
    if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
        return fmt.Errorf("storage: MkdirAll(%q): %w", filepath.Dir(target), err)
    }
    return os.Rename(localPath, target)
}

func (s *LocalDiskStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
    ok, err := s.Has(ctx, key)
    if err != nil {
        return nil, fmt.Errorf("storage: Has %s: %w", key, err)
    }
    if !ok {
        return nil, fmt.Errorf("storage: %s: %w", key, os.ErrNotExist)
    }
    return os.Open(s.absPath(key))
}

// SignGet and SignPut return "" for now; wired in Task 4.
func (s *LocalDiskStorage) SignGet(context.Context, string) (string, error) { return "", nil }
func (s *LocalDiskStorage) SignPut(context.Context, string) (string, error) { return "", nil }

// FilesystemPathForLocalProcessor is a transitional escape hatch for code paths
// that still call the path-based ProcessorClient. Removed in plan #4 once Python
// is switched to URL handoff.
func (s *LocalDiskStorage) FilesystemPathForLocalProcessor(key string) string {
    return s.absPath(key)
}
```

- [ ] **Step 4: Run tests, expect pass for new tests**

```
cd backend && go test ./services/ -run TestLocalDiskStorage -v
```

Expected: PASS. Other packages still fail to build — that's Task 2.

- [ ] **Step 5: Commit**

```bash
git add backend/services/storage.go backend/services/storage_test.go
git commit -m "refactor(storage): switch Storage interface to key-based API"
```

---

## Task 2: Update all handlers and tests to use key-based API

**Goal:** Make the rest of the backend compile and tests pass with the new interface. Replace every `LocalPath(ctx, videoID, name)` call site with `key := storage.Key(videoID, name)`, and replace `os.Open(path)` blocks with `storage.Open(ctx, key)`.

**Files:**
- Modify: `backend/api/handlers/audio.go`, `melody.go`, `preview_audio.go`, `preview_melody.go`, `preview_stems.go`, `preview_shift.go`, `preview_key.go`, `preview.go`, `generate.go`
- Modify: corresponding `*_test.go` files (replace test doubles)
- Modify: `backend/services/job_runner.go`, `backend/services/processor.go` callsites if they touch `LocalPath` (use `FilesystemPathForLocalProcessor` for path-based processor calls)

- [ ] **Step 1: Find every caller of the old API**

```
cd backend && grep -rn "storage.LocalPath\|\.LocalPath(\|storage.Has(ctx, " --include="*.go" .
```

Expected: list of files that need editing.

- [ ] **Step 2: Update each handler — pattern**

For each handler, replace this pattern:

```go
// OLD
ok, err := storage.Has(ctx, videoID, name)
if !ok { /* 404 */ }
path, err := storage.LocalPath(ctx, videoID, name)
f, err := os.Open(path)
defer f.Close()
// ...read f or http.ServeContent
```

with:

```go
// NEW
key := storage.Key(videoID, name)
ok, err := storage.Has(ctx, key)
if !ok { /* 404 */ }
rc, err := storage.Open(ctx, key)
defer rc.Close()
// ...read rc directly
```

For `audio.go` (which uses `http.ServeContent` for Range support), the streaming behavior must be preserved. Since `R2Storage.Open` returns `io.ReadCloser` (not a ReadSeeker), use this shim in `audio.go`:

```go
// Buffer to memory; full instrumental MP3s are bounded (~10MB). Avoids
// needing ReadSeeker for Range requests.
buf, err := io.ReadAll(rc)
if err != nil { /* 500 */ }
http.ServeContent(w, r, "audio.mp3", time.Now(), bytes.NewReader(buf))
```

(Better-engineered alternatives like streaming Range from R2 can come later if profiling shows memory pressure. Beta scale; not now.)

- [ ] **Step 3: Update path-based ProcessorClient call sites**

Anywhere we currently pass `LocalPath(...)` results to `processor.Shift / Separate / Melody / PreviewKey`, switch to the transitional helper:

```go
// Only safe in STORAGE_BACKEND=local mode. Plan #4 replaces this with URL handoff.
local, ok := storage.(*services.LocalDiskStorage)
if !ok {
    return errors.New("processor calls require STORAGE_BACKEND=local until plan #4")
}
inputPath := local.FilesystemPathForLocalProcessor(local.Key(videoID, "full.mp3"))
// ...
```

Centralize this assertion behind a helper if it appears 3+ times — otherwise inline is fine.

- [ ] **Step 4: Update handler tests to use new Storage API**

Test doubles that implemented the old `Storage` interface must be updated. Most tests use `NewLocalDiskStorage(t.TempDir())` directly — those just need their `Has/Commit` call sites updated.

- [ ] **Step 5: Build and run all tests**

```
cd backend && go build ./... && go test ./...
```

Expected: PASS. If any handler test fails because the test was setting up cached files via the old API, fix the setup.

- [ ] **Step 6: Smoke test locally**

```
cd backend && go run ./cmd/server &
# in another shell:
curl -s "localhost:8080/api/songs/search?q=hello" | jq '.[0]'
# then exercise preview / generate on a known song to confirm end-to-end
```

Expected: same behavior as before the refactor.

- [ ] **Step 7: Commit**

```bash
git add backend/
git commit -m "refactor(backend): switch all handlers to key-based Storage API"
```

---

## Task 3: Add config fields for `STORAGE_BACKEND`, R2, and blob base URL

**Goal:** Extend `config.Config` with the env vars from spec §8. Validation: when `STORAGE_BACKEND=r2`, all R2 fields are required; when `local`, only `BlobBaseURL` is required (defaults to `http://localhost:8080`).

**Files:**
- Modify: `backend/config/config.go`
- Modify: `backend/config/config_test.go`

- [ ] **Step 1: Write failing tests for new fields**

```go
// backend/config/config_test.go

func TestLoad_storageBackend_local_defaults(t *testing.T) {
    t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))
    t.Setenv("STORAGE_BACKEND", "local")
    cfg, err := Load()
    require.NoError(t, err)
    require.Equal(t, "local", cfg.StorageBackend)
    require.Equal(t, "http://localhost:8080", cfg.BlobBaseURL)
}

func TestLoad_storageBackend_r2_requiresR2Fields(t *testing.T) {
    t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))
    t.Setenv("STORAGE_BACKEND", "r2")
    _, err := Load()
    require.Error(t, err)
    require.Contains(t, err.Error(), "R2_ACCOUNT_ID")
}

func TestLoad_storageBackend_r2_complete(t *testing.T) {
    t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))
    t.Setenv("STORAGE_BACKEND", "r2")
    t.Setenv("R2_ACCOUNT_ID", "acct")
    t.Setenv("R2_ACCESS_KEY_ID", "key")
    t.Setenv("R2_SECRET_ACCESS_KEY", "secret")
    t.Setenv("R2_BUCKET", "cantus-cache")
    cfg, err := Load()
    require.NoError(t, err)
    require.Equal(t, "r2", cfg.StorageBackend)
    require.Equal(t, 600, cfg.R2PresignTTLSeconds) // default
}

func TestLoad_storageBackend_invalid(t *testing.T) {
    t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))
    t.Setenv("STORAGE_BACKEND", "s3")
    _, err := Load()
    require.Error(t, err)
    require.Contains(t, err.Error(), "STORAGE_BACKEND")
}
```

- [ ] **Step 2: Run tests, expect fail**

```
cd backend && go test ./config/ -run TestLoad_storageBackend -v
```

Expected: FAIL (fields don't exist).

- [ ] **Step 3: Extend Config struct + Load**

```go
type Config struct {
    // ...existing fields...

    StorageBackend       string // STORAGE_BACKEND, "local" or "r2"; default "local"
    BlobBaseURL          string // BLOB_BASE_URL, default "http://localhost:8080" (local mode only)
    R2AccountID          string // R2_ACCOUNT_ID, required if r2
    R2AccessKeyID        string // R2_ACCESS_KEY_ID, required if r2
    R2SecretAccessKey    string // R2_SECRET_ACCESS_KEY, required if r2
    R2Bucket             string // R2_BUCKET, required if r2
    R2PresignTTLSeconds  int    // R2_PRESIGN_TTL_SECONDS, default 600
}

// In Load(), after existing validation:
cfg.StorageBackend = getEnvString("STORAGE_BACKEND", "local")
switch cfg.StorageBackend {
case "local":
    cfg.BlobBaseURL = getEnvString("BLOB_BASE_URL", "http://localhost:8080")
case "r2":
    required := map[string]*string{
        "R2_ACCOUNT_ID":        &cfg.R2AccountID,
        "R2_ACCESS_KEY_ID":     &cfg.R2AccessKeyID,
        "R2_SECRET_ACCESS_KEY": &cfg.R2SecretAccessKey,
        "R2_BUCKET":            &cfg.R2Bucket,
    }
    var missing []string
    for env, dest := range required {
        v := os.Getenv(env)
        if v == "" {
            missing = append(missing, env)
        }
        *dest = v
    }
    if len(missing) > 0 {
        return nil, fmt.Errorf("missing required env vars for STORAGE_BACKEND=r2: %v", missing)
    }
    if cfg.R2PresignTTLSeconds, err = getEnvInt("R2_PRESIGN_TTL_SECONDS", 600); err != nil {
        return nil, err
    }
default:
    return nil, fmt.Errorf("STORAGE_BACKEND: %q is not one of local/r2", cfg.StorageBackend)
}
```

- [ ] **Step 4: Tests pass**

```
cd backend && go test ./config/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/config/
git commit -m "config: add STORAGE_BACKEND, R2, and BLOB_BASE_URL env vars"
```

---

## Task 4: Add blob token helper and `/internal/blob/{key}` handler

**Goal:** Implement the local-mode URL handoff. `LocalDiskStorage.SignGet/SignPut` will produce URLs like `http://localhost:8080/internal/blob/abc12345678/melody.json?token=<hmac>&exp=<unix>&op=get`. The `/internal/blob/{key}` handler verifies the token and serves/accepts files from disk.

**Files:**
- Create: `backend/services/storage_blob_token.go`
- Create: `backend/services/storage_blob_token_test.go`
- Create: `backend/api/handlers/blob.go`
- Create: `backend/api/handlers/blob_test.go`
- Modify: `backend/services/storage.go` (wire `LocalDiskStorage.SignGet/SignPut`)

- [ ] **Step 1: Write failing tests for blob token sign/verify**

```go
// backend/services/storage_blob_token_test.go

func TestBlobToken_signAndVerify(t *testing.T) {
    signer, err := NewSigner(strings.Repeat("k", 32))
    require.NoError(t, err)

    bt := NewBlobTokener(signer)
    now := time.Unix(1700000000, 0)
    exp := now.Add(5 * time.Minute)

    token := bt.Sign("abc/melody.json", "get", exp)

    require.NoError(t, bt.Verify("abc/melody.json", "get", token, exp.Unix(), now))
}

func TestBlobToken_rejectsExpired(t *testing.T) {
    signer, _ := NewSigner(strings.Repeat("k", 32))
    bt := NewBlobTokener(signer)
    now := time.Unix(1700000000, 0)
    exp := now.Add(-1 * time.Second) // already expired
    token := bt.Sign("abc/melody.json", "get", exp)
    err := bt.Verify("abc/melody.json", "get", token, exp.Unix(), now)
    require.ErrorIs(t, err, ErrBlobTokenExpired)
}

func TestBlobToken_rejectsWrongOp(t *testing.T) {
    signer, _ := NewSigner(strings.Repeat("k", 32))
    bt := NewBlobTokener(signer)
    now := time.Unix(1700000000, 0)
    exp := now.Add(5 * time.Minute)
    token := bt.Sign("abc/melody.json", "get", exp)
    err := bt.Verify("abc/melody.json", "put", token, exp.Unix(), now)
    require.ErrorIs(t, err, ErrBlobTokenInvalid)
}
```

- [ ] **Step 2: Run tests, expect fail (undefined)**

```
cd backend && go test ./services/ -run TestBlobToken -v
```

- [ ] **Step 3: Implement BlobTokener**

```go
// backend/services/storage_blob_token.go

var (
    ErrBlobTokenInvalid = errors.New("blob: invalid token")
    ErrBlobTokenExpired = errors.New("blob: token expired")
)

type BlobTokener struct {
    signer *Signer
}

func NewBlobTokener(signer *Signer) *BlobTokener {
    return &BlobTokener{signer: signer}
}

// payload is `key|op|exp` (op is "get"|"put"); HMAC reuses VIDEO_ID_SIGNING_KEY.
func (b *BlobTokener) payload(key, op string, expUnix int64) string {
    return fmt.Sprintf("%s|%s|%d", key, op, expUnix)
}

func (b *BlobTokener) Sign(key, op string, exp time.Time) string {
    return b.signer.Sign(b.payload(key, op, exp.Unix()))
}

func (b *BlobTokener) Verify(key, op, token string, expUnix int64, now time.Time) error {
    if now.Unix() > expUnix {
        return ErrBlobTokenExpired
    }
    if !b.signer.Valid(b.payload(key, op, expUnix), token) {
        return ErrBlobTokenInvalid
    }
    return nil
}
```

- [ ] **Step 4: Tests pass**

```
cd backend && go test ./services/ -run TestBlobToken -v
```

- [ ] **Step 5: Write failing tests for /internal/blob handler**

```go
// backend/api/handlers/blob_test.go

func TestBlob_GET_returnsFile(t *testing.T) {
    s, _ := services.NewLocalDiskStorage(t.TempDir())
    signer, _ := services.NewSigner(strings.Repeat("k", 32))
    bt := services.NewBlobTokener(signer)

    // Seed a file via Commit.
    src := filepath.Join(t.TempDir(), "src.bin")
    require.NoError(t, os.WriteFile(src, []byte("hello"), 0o644))
    key := s.Key("abc12345678", "melody.json")
    require.NoError(t, s.Commit(context.Background(), key, src))

    exp := time.Now().Add(5 * time.Minute)
    token := bt.Sign(key, "get", exp)

    r := httptest.NewRequest(http.MethodGet,
        "/internal/blob/"+key+"?op=get&exp="+strconv.FormatInt(exp.Unix(), 10)+"&token="+token, nil)
    w := httptest.NewRecorder()
    chiCtx := chi.NewRouteContext()
    chiCtx.URLParams.Add("*", key)
    r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chiCtx))

    handlers.Blob(s, bt)(w, r)
    require.Equal(t, http.StatusOK, w.Code)
    require.Equal(t, "hello", w.Body.String())
}

func TestBlob_GET_rejectsBadToken(t *testing.T) {
    s, _ := services.NewLocalDiskStorage(t.TempDir())
    signer, _ := services.NewSigner(strings.Repeat("k", 32))
    bt := services.NewBlobTokener(signer)
    key := s.Key("abc12345678", "melody.json")
    exp := time.Now().Add(5 * time.Minute)
    r := httptest.NewRequest(http.MethodGet,
        "/internal/blob/"+key+"?op=get&exp="+strconv.FormatInt(exp.Unix(), 10)+"&token=deadbeef", nil)
    w := httptest.NewRecorder()
    chiCtx := chi.NewRouteContext()
    chiCtx.URLParams.Add("*", key)
    r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chiCtx))
    handlers.Blob(s, bt)(w, r)
    require.Equal(t, http.StatusForbidden, w.Code)
}

func TestBlob_PUT_writesFile(t *testing.T) {
    s, _ := services.NewLocalDiskStorage(t.TempDir())
    signer, _ := services.NewSigner(strings.Repeat("k", 32))
    bt := services.NewBlobTokener(signer)
    key := s.Key("abc12345678", "melody.json")
    exp := time.Now().Add(5 * time.Minute)
    token := bt.Sign(key, "put", exp)
    r := httptest.NewRequest(http.MethodPut,
        "/internal/blob/"+key+"?op=put&exp="+strconv.FormatInt(exp.Unix(), 10)+"&token="+token,
        strings.NewReader("payload"))
    w := httptest.NewRecorder()
    chiCtx := chi.NewRouteContext()
    chiCtx.URLParams.Add("*", key)
    r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chiCtx))
    handlers.Blob(s, bt)(w, r)
    require.Equal(t, http.StatusNoContent, w.Code)
    has, _ := s.Has(context.Background(), key)
    require.True(t, has)
}

func TestBlob_rejectsExpiredToken(t *testing.T) {
    s, _ := services.NewLocalDiskStorage(t.TempDir())
    signer, _ := services.NewSigner(strings.Repeat("k", 32))
    bt := services.NewBlobTokener(signer)
    key := s.Key("abc12345678", "melody.json")
    exp := time.Now().Add(-1 * time.Second)
    token := bt.Sign(key, "get", exp)
    r := httptest.NewRequest(http.MethodGet,
        "/internal/blob/"+key+"?op=get&exp="+strconv.FormatInt(exp.Unix(), 10)+"&token="+token, nil)
    w := httptest.NewRecorder()
    chiCtx := chi.NewRouteContext()
    chiCtx.URLParams.Add("*", key)
    r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chiCtx))
    handlers.Blob(s, bt)(w, r)
    require.Equal(t, http.StatusForbidden, w.Code)
}
```

- [ ] **Step 6: Implement Blob handler**

```go
// backend/api/handlers/blob.go

func Blob(storage services.Storage, bt *services.BlobTokener) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        key := chi.URLParam(r, "*")
        op := r.URL.Query().Get("op")
        token := r.URL.Query().Get("token")
        expStr := r.URL.Query().Get("exp")

        expUnix, err := strconv.ParseInt(expStr, 10, 64)
        if err != nil {
            http.Error(w, "bad exp", http.StatusBadRequest)
            return
        }
        if err := bt.Verify(key, op, token, expUnix, time.Now()); err != nil {
            http.Error(w, err.Error(), http.StatusForbidden)
            return
        }

        switch op {
        case "get":
            rc, err := storage.Open(r.Context(), key)
            if err != nil { http.Error(w, "not found", http.StatusNotFound); return }
            defer rc.Close()
            io.Copy(w, rc)
        case "put":
            // Stage incoming body to a temp file, then Commit.
            tmp, err := os.CreateTemp("", "blob-*")
            if err != nil { http.Error(w, "stage", http.StatusInternalServerError); return }
            defer os.Remove(tmp.Name())
            if _, err := io.Copy(tmp, r.Body); err != nil { http.Error(w, "stage", http.StatusInternalServerError); return }
            tmp.Close()
            if err := storage.Commit(r.Context(), key, tmp.Name()); err != nil {
                http.Error(w, "commit", http.StatusInternalServerError); return
            }
            w.WriteHeader(http.StatusNoContent)
        default:
            http.Error(w, "bad op", http.StatusBadRequest)
        }
    }
}
```

- [ ] **Step 7: Wire LocalDiskStorage.SignGet/SignPut**

```go
// backend/services/storage.go — replace placeholder stubs

type LocalDiskStorage struct {
    root        string
    blobBaseURL string
    tokener     *BlobTokener
    ttl         time.Duration
}

func NewLocalDiskStorage(root, blobBaseURL string, tokener *BlobTokener, ttl time.Duration) (*LocalDiskStorage, error) {
    // (existing dir setup unchanged)
    return &LocalDiskStorage{root: absRoot, blobBaseURL: blobBaseURL, tokener: tokener, ttl: ttl}, nil
}

func (s *LocalDiskStorage) signURL(key, op string) (string, error) {
    exp := time.Now().Add(s.ttl)
    token := s.tokener.Sign(key, op, exp)
    u := fmt.Sprintf("%s/internal/blob/%s?op=%s&exp=%d&token=%s",
        s.blobBaseURL, key, op, exp.Unix(), token)
    return u, nil
}

func (s *LocalDiskStorage) SignGet(_ context.Context, key string) (string, error) { return s.signURL(key, "get") }
func (s *LocalDiskStorage) SignPut(_ context.Context, key string) (string, error) { return s.signURL(key, "put") }
```

Update existing tests for `NewLocalDiskStorage` to pass the new args (use a real `BlobTokener` built from a test signer; `blobBaseURL` = `"http://test"`).

- [ ] **Step 8: Run all tests**

```
cd backend && go test ./...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/
git commit -m "feat(storage): add blob token + /internal/blob handler for local URL handoff"
```

---

## Task 5: Mount `/internal/blob/*` route in router

**Goal:** Wire the Blob handler into chi. Only mount in `STORAGE_BACKEND=local` mode (in `r2` mode the route shouldn't exist — defense in depth).

**Files:**
- Modify: `backend/api/router.go`
- Modify: `backend/cmd/server/main.go` (pass tokener)

- [ ] **Step 1: Add optional Blob handler arg to NewRouter**

```go
// router.go
func NewRouter(
    allowedOrigins []string,
    log zerolog.Logger,
    svc services.YouTubeService,
    signer *services.Signer,
    storage services.Storage,
    processor services.ProcessorClient,
    jobRunner services.JobSubmitter,
    jobStore *services.JobStore,
    blobTokener *services.BlobTokener, // nil in r2 mode
) *chi.Mux {
    // ...
    if blobTokener != nil {
        mux.HandleFunc("/internal/blob/*", handlers.Blob(storage, blobTokener))
    }
    // ...
}
```

(Use `HandleFunc` with `*` wildcard to capture nested keys like `abc/shifted/0/audio.mp3`.)

- [ ] **Step 2: Wire from main.go**

```go
// cmd/server/main.go
var blobTokener *services.BlobTokener
if cfg.StorageBackend == "local" {
    blobTokener = services.NewBlobTokener(signer)
}
router := api.NewRouter(..., blobTokener)
```

- [ ] **Step 3: Add router test for mounting**

```go
// router_test.go
func TestRouter_blobRoute_mountedOnlyInLocalMode(t *testing.T) {
    // build router with blobTokener=nil → request returns 404
    // build router with blobTokener!=nil → request reaches handler (returns 400/403, not 404)
}
```

- [ ] **Step 4: Run tests**

```
cd backend && go test ./...
```

- [ ] **Step 5: Smoke test**

```
cd backend && STORAGE_BACKEND=local go run ./cmd/server
# in another shell, sign a URL via a small Go script or by hand, GET it.
# Easier: run the existing full flow (preview / generate) which now passes through blob handler internally.
```

Expected: end-to-end flows still work; existing behavior unchanged.

- [ ] **Step 6: Commit**

```bash
git add backend/
git commit -m "feat(router): mount /internal/blob route in local mode"
```

---

## Task 6: Implement `R2Storage`

**Goal:** Production Storage impl using AWS SDK v2 against the R2 endpoint. Implements all interface methods.

**Files:**
- Create: `backend/services/storage_r2.go`
- Create: `backend/services/storage_r2_test.go`
- Modify: `backend/go.mod` (add `aws-sdk-go-v2/service/s3`, `aws-sdk-go-v2/credentials`, `aws-sdk-go-v2/config`)

- [ ] **Step 1: Add SDK deps**

```
cd backend && go get github.com/aws/aws-sdk-go-v2/service/s3 \
    github.com/aws/aws-sdk-go-v2/credentials \
    github.com/aws/aws-sdk-go-v2/config
```

- [ ] **Step 2: Write failing tests using `httptest.Server` mocking R2 endpoint**

```go
// backend/services/storage_r2_test.go

func TestR2Storage_Has_returnsTrueOnHEAD200WithSize(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        require.Equal(t, http.MethodHead, r.Method)
        w.Header().Set("Content-Length", "123")
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    s, err := NewR2Storage(R2Config{
        AccountID: "acct", AccessKeyID: "k", SecretAccessKey: "s",
        Bucket: "cantus-cache", PresignTTL: time.Minute, Endpoint: srv.URL,
    })
    require.NoError(t, err)

    has, err := s.Has(context.Background(), "abc/melody.json")
    require.NoError(t, err)
    require.True(t, has)
}

func TestR2Storage_Has_returnsFalseOnZeroSize(t *testing.T) { /* HEAD 200 Content-Length: 0 */ }
func TestR2Storage_Has_returnsFalseOn404(t *testing.T) { /* HEAD 404 */ }

func TestR2Storage_SignGet_returnsPresignedURL(t *testing.T) {
    s, _ := NewR2Storage(R2Config{AccountID: "acct", AccessKeyID: "k", SecretAccessKey: "s", Bucket: "b", PresignTTL: time.Minute, Endpoint: "https://r2.example"})
    u, err := s.SignGet(context.Background(), "abc/melody.json")
    require.NoError(t, err)
    require.Contains(t, u, "abc/melody.json")
    require.Contains(t, u, "X-Amz-Signature")
}

func TestR2Storage_Key_isPrefixedPath(t *testing.T) { /* same shape as LocalDiskStorage */ }
```

- [ ] **Step 3: Implement R2Storage**

```go
// backend/services/storage_r2.go

type R2Config struct {
    AccountID         string
    AccessKeyID       string
    SecretAccessKey   string
    Bucket            string
    PresignTTL        time.Duration
    Endpoint          string // override for tests; in prod derived from AccountID
}

type R2Storage struct {
    cfg       R2Config
    client    *s3.Client
    presigner *s3.PresignClient
}

func NewR2Storage(cfg R2Config) (*R2Storage, error) {
    endpoint := cfg.Endpoint
    if endpoint == "" {
        endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)
    }

    awsCfg, err := config.LoadDefaultConfig(context.Background(),
        config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
            cfg.AccessKeyID, cfg.SecretAccessKey, "")),
        config.WithRegion("auto"),
    )
    if err != nil {
        return nil, err
    }

    client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
        o.BaseEndpoint = aws.String(endpoint)
        o.UsePathStyle = true
    })
    return &R2Storage{
        cfg:       cfg,
        client:    client,
        presigner: s3.NewPresignClient(client),
    }, nil
}

func (s *R2Storage) Key(videoID, name string) string { return path.Join(videoID, name) }

func (s *R2Storage) Has(ctx context.Context, key string) (bool, error) {
    out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
        Bucket: &s.cfg.Bucket, Key: &key,
    })
    if err != nil {
        var nf *types.NotFound
        if errors.As(err, &nf) {
            return false, nil
        }
        return false, err
    }
    return out.ContentLength != nil && *out.ContentLength > 0, nil
}

func (s *R2Storage) SignGet(ctx context.Context, key string) (string, error) {
    req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
        Bucket: &s.cfg.Bucket, Key: &key,
    }, s3.WithPresignExpires(s.cfg.PresignTTL))
    if err != nil { return "", err }
    return req.URL, nil
}

func (s *R2Storage) SignPut(ctx context.Context, key string) (string, error) {
    req, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
        Bucket: &s.cfg.Bucket, Key: &key,
    }, s3.WithPresignExpires(s.cfg.PresignTTL))
    if err != nil { return "", err }
    return req.URL, nil
}

func (s *R2Storage) Commit(ctx context.Context, key, localPath string) error {
    // Used for Go-side uploads (e.g., yt-dlp result). Python uploads go through SignPut.
    f, err := os.Open(localPath)
    if err != nil { return err }
    defer f.Close()
    _, err = s.client.PutObject(ctx, &s3.PutObjectInput{
        Bucket: &s.cfg.Bucket, Key: &key, Body: f,
    })
    return err
}

func (s *R2Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
    out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
        Bucket: &s.cfg.Bucket, Key: &key,
    })
    if err != nil { return nil, err }
    return out.Body, nil
}
```

- [ ] **Step 4: Tests pass**

```
cd backend && go test ./services/ -run TestR2Storage -v
```

- [ ] **Step 5: Commit**

```bash
git add backend/
git commit -m "feat(storage): add R2Storage implementation using aws-sdk-go-v2"
```

---

## Task 7: Wire backend selection in `main.go`

**Goal:** Switch on `cfg.StorageBackend` to construct the chosen impl. Pass `blobTokener` only in local mode.

**Files:**
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Replace direct `NewLocalDiskStorage` call with switch**

```go
// cmd/server/main.go

signer, err := services.NewSigner(cfg.VideoIDSigningKey)
if err != nil { /* fail */ }

var storage services.Storage
var blobTokener *services.BlobTokener

switch cfg.StorageBackend {
case "local":
    blobTokener = services.NewBlobTokener(signer)
    s, err := services.NewLocalDiskStorage(cfg.CacheDir, cfg.BlobBaseURL, blobTokener, 10*time.Minute)
    if err != nil { /* fail */ }
    storage = s
case "r2":
    s, err := services.NewR2Storage(services.R2Config{
        AccountID:       cfg.R2AccountID,
        AccessKeyID:     cfg.R2AccessKeyID,
        SecretAccessKey: cfg.R2SecretAccessKey,
        Bucket:          cfg.R2Bucket,
        PresignTTL:      time.Duration(cfg.R2PresignTTLSeconds) * time.Second,
    })
    if err != nil { /* fail */ }
    storage = s
default:
    log.Fatal().Str("backend", cfg.StorageBackend).Msg("unknown STORAGE_BACKEND")
}

router := api.NewRouter(..., storage, ..., blobTokener)
```

- [ ] **Step 2: Build and run with each backend**

```
cd backend && STORAGE_BACKEND=local go run ./cmd/server   # should boot
cd backend && STORAGE_BACKEND=r2 R2_ACCOUNT_ID=fake R2_ACCESS_KEY_ID=k \
    R2_SECRET_ACCESS_KEY=s R2_BUCKET=b go run ./cmd/server   # should boot
```

Expected: both boot. R2 mode will fail on actual generate calls because Task 2's transitional path-based processor logic only works in local mode — that's expected and documented.

- [ ] **Step 3: Commit**

```bash
git add backend/
git commit -m "feat(server): select Storage impl via STORAGE_BACKEND"
```

---

## Task 8: Update `.env.example` and docs

**Goal:** Make the new config surface discoverable.

**Files:**
- Modify: `backend/.env.example`
- Modify: `CLAUDE.md` (Architecture section — note the new Storage abstraction)

- [ ] **Step 1: Add new vars to `.env.example`**

```
# Storage backend selection. "local" uses tmp/cache/ + /internal/blob; "r2" uses Cloudflare R2.
STORAGE_BACKEND=local
BLOB_BASE_URL=http://localhost:8080

# R2 (only required when STORAGE_BACKEND=r2):
# R2_ACCOUNT_ID=
# R2_ACCESS_KEY_ID=
# R2_SECRET_ACCESS_KEY=
# R2_BUCKET=cantus-cache
# R2_PRESIGN_TTL_SECONDS=600
```

- [ ] **Step 2: Update CLAUDE.md Storage notes**

Replace the existing "Storage interface" bullet with:

> `Storage` interface in `backend/services/storage.go`: handlers operate on opaque keys via `Key/Has/SignGet/SignPut/Commit/Open`. `LocalDiskStorage` uses `tmp/cache/` with a `/internal/blob/{key}` route for URL handoff. `R2Storage` uses Cloudflare R2 via aws-sdk-go-v2. Select impl via `STORAGE_BACKEND`. Note: in `r2` mode, generate flows still require local-mode behavior until plan #4 lands processor URL handoff.

- [ ] **Step 3: Commit**

```bash
git add backend/.env.example CLAUDE.md
git commit -m "docs: document STORAGE_BACKEND and new storage env vars"
```

---

## Self-review checklist (engineer running this plan)

Before opening PR / merging:

1. `go build ./...` from `backend/` succeeds.
2. `go test ./...` from `backend/` passes (no skipped tests).
3. `STORAGE_BACKEND=local` end-to-end: search → preview → preview-shift → generate → audio → melody all work.
4. `STORAGE_BACKEND=r2` boots without error against fake R2 creds.
5. No remaining `LocalPath` references outside `LocalDiskStorage.FilesystemPathForLocalProcessor`.
6. Grep for `os.Open` in `backend/api/handlers/` — should be gone (replaced by `storage.Open`).
7. `.env.example` lists every new var.
