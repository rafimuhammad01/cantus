# Processor URL Handoff + ProcessorClient Split

**Date:** 2026-06-14
**Status:** Design locked. Implementation plan pending.
**Related:** `docs/superpowers/specs/2026-06-13-deployment-design.md` §6, `docs/superpowers/plans/2026-06-13-go-storage-refactor.md` (Plan #1, shipped), `CLAUDE.md`.

---

## 1. Goal

Complete the Go-side of deployment spec step 1. Replace the path-based `ProcessorClient` with two URL-based interfaces (`CPUProcessorClient`, `GPUProcessorClient`). Python services receive presigned URLs, download → process → upload. Remove the `FilesystemPathForLocalProcessor` escape hatch left by Plan #1 so `STORAGE_BACKEND=r2` works end-to-end.

## 2. Non-goals

- Splitting Python into separate CPU and GPU services (that is deployment spec step 2, next plan).
- Moving Search from `YouTubeService` into `CPUProcessorClient` (deferred to the Python split plan — Search doesn't take audio paths, so URL handoff doesn't apply).
- Sig payload change to `videoID|sessionID|issuedAt`.
- Dockerfiles, Fly/HF deployment, Turnstile, WAF.
- Cache eviction.

## 3. Target shapes

### 3.1 Go interfaces

```go
// backend/services/processor.go

type CPUProcessorClient interface {
    Shift(ctx context.Context, inputURL, outputURL string, semitones float64) error
    PreviewKey(ctx context.Context, inputURL string) (string, error)
}

type GPUProcessorClient interface {
    Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error
    Melody(ctx context.Context, vocalsInputURL, outputURL string) error
}
```

Two concrete types — `PythonCPUProcessorClient` and `PythonGPUProcessorClient` — each constructed with its own base URL and `http.Client`. CPU client gets a 30s overall timeout; GPU client 180s (with separate dial vs read timeouts; HF cold start can be 30s — sets the floor for dial).

The combined `ProcessorClient` interface and `PythonProcessorClient` type are removed.

### 3.2 Storage interface addition

```go
// backend/services/storage.go

type Storage interface {
    // ... existing methods ...

    // Verify reports nil if the object exists with non-zero size, otherwise
    // ErrObjectNotMaterialized. Used after Python returns 200 to confirm the
    // upload actually landed in R2.
    Verify(ctx context.Context, key string) error
}

var ErrObjectNotMaterialized = errors.New("storage: object not materialized")
```

Both `LocalDiskStorage` and `R2Storage` implement `Verify` as a thin wrapper around `Has`. No additional network calls beyond the existing HEAD/stat.

### 3.3 Python request schemas

| Endpoint | Old body | New body |
|---|---|---|
| `POST /shift` | `{input_path, output_path, semitones}` | `{input_url, output_url, semitones}` |
| `POST /separate` | `{input_path, output_dir}` | `{input_url, vocals_output_url, no_vocals_output_url}` |
| `POST /melody` | `{vocals_path, output_path}` | `{vocals_input_url, output_url}` |
| `POST /preview-key` | `{input_path}` | `{input_url}` |

`POST /separate` response no longer needs to return paths (Go already knows the keys it signed). Returns `204 No Content` on success.

## 4. Architecture

### 4.1 Caller flow (Go-side)

```
Handler / JobRunner
    inKey  := storage.Key(videoID, "input.wav")
    outKey := storage.Key(videoID, "output.wav")
    inURL,  _ := storage.SignGet(ctx, inKey)
    outURL, _ := storage.SignPut(ctx, outKey)
    if err := cpu.Shift(ctx, inURL, outURL, semitones); err != nil { ... }
    if err := storage.Verify(ctx, outKey); err != nil { ... }
```

For `Separate`, two PUT URLs are minted upfront (vocals + no_vocals). Both are verified after Python returns.

### 4.2 Handler flow (Python-side)

Each handler:
1. Downloads `input_url` to a scratch tempfile via streaming GET.
2. Runs the existing service code on the tempfile (unchanged business logic).
3. PUTs the result tempfile to `output_url` via streaming PUT.
4. Cleans up the scratch dir in a `finally`.

Failures (download error, processing error, upload error) propagate as `HTTPException(502)` with detail. No retries inside Python (per design decision Q3 — fail fast, let Go reschedule). `PreviewKey` has no upload step.

### 4.3 Shared Python helper

`audio-processor/routers/_io_url.py`:

```python
async def download_to_temp(url: str, scratch: Path) -> Path:
    """Stream GET url → scratch / 'in.bin'. Returns the path.
    Raises HTTPException(502) on any HTTP or network error.
    httpx timeout: 60s total."""

async def upload_from_path(path: Path, url: str) -> None:
    """Stream PUT path → url. Raises HTTPException(502) on failure.
    httpx timeout: 60s total."""
```

Helpers are async because httpx's streaming API is async; routers stay sync at the FastAPI level by wrapping in `asyncio.run` per call (existing pattern — see how routers/separate.py runs Demucs).

### 4.4 Local-mode parity

`LocalDiskStorage.SignGet/SignPut` already mint `http://localhost:8080/internal/blob/{key}?op=...&exp=...&token=...` URLs via the blob handler shipped in Plan #1. Python's helpers don't branch on backend — same protocol. No code path is R2-specific.

## 5. Configuration

New env vars on Go backend:

| Var | Purpose | Default |
|---|---|---|
| `CPU_PROCESSOR_URL` | Base URL for CPU client (Shift, PreviewKey) | falls back to `PROCESSOR_URL` if unset |
| `GPU_PROCESSOR_URL` | Base URL for GPU client (Separate, Melody) | falls back to `PROCESSOR_URL` if unset |
| `CPU_PROCESSOR_TIMEOUT_SECONDS` | Overall HTTP timeout for CPU client | 30 |
| `GPU_PROCESSOR_TIMEOUT_SECONDS` | Overall HTTP timeout for GPU client | 180 |

During this plan both `CPU_PROCESSOR_URL` and `GPU_PROCESSOR_URL` point at the same Python service. The Python split plan will diverge them. The existing `PROCESSOR_URL` env var continues to work as a fallback so existing `.env` files don't break.

## 6. File-level change list

### Modify

- `backend/services/processor.go` — delete combined `ProcessorClient`; add `CPUProcessorClient` + `GPUProcessorClient` interfaces and `PythonCPUProcessorClient` + `PythonGPUProcessorClient` types.
- `backend/services/processor_test.go` — rewrite for two clients; assert URL fields in request bodies; assert each client uses its own timeout.
- `backend/services/storage.go` — add `Verify` to interface + both impls; export `ErrObjectNotMaterialized`.
- `backend/services/storage_test.go`, `backend/services/storage_r2_test.go` — `Verify` coverage.
- `backend/services/storage.go` — remove `FilesystemPathForLocalProcessor`.
- `backend/services/job_runner.go` — switch Separate/Melody/Shift call sites to URL-based; remove `*LocalDiskStorage` type assertions; add `Verify` after each Python call.
- `backend/services/job_runner_test.go` — update fakes for the two new client interfaces.
- `backend/api/handlers/preview_shift.go`, `preview_stems.go`, `preview_key.go` — same caller-pattern change as `job_runner.go`.
- `backend/api/handlers/*_test.go` — update handler test doubles for new client interfaces.
- `backend/api/router.go` — `NewRouter` takes `cpu CPUProcessorClient, gpu GPUProcessorClient` instead of one `ProcessorClient`.
- `backend/cmd/server/main.go` — construct two clients; pass to router.
- `backend/config/config.go` + tests — new env vars.
- `backend/.env.example` — document new vars; mark `PROCESSOR_URL` as legacy fallback.
- `audio-processor/routers/shift.py`, `separate.py`, `melody.py`, `preview_key.py` — switch Pydantic schemas; rewrite handler bodies to use `_io_url` helpers.
- `audio-processor/tests/*` — update fixtures for new schemas; use `respx` to mock httpx.
- `CLAUDE.md` — update Architecture section (Python takes URLs not paths).

### Create

- `audio-processor/routers/_io_url.py` — shared download/upload helpers.
- `audio-processor/tests/test_io_url.py` — unit tests for the helpers (success + 502 propagation).

### Delete (after migration)

- `backend/services/storage.go`: `FilesystemPathForLocalProcessor` method and all 9 call sites.
- `backend/services/processor.go`: old combined `ProcessorClient` interface + `PythonProcessorClient` type.

`PROCESSOR_URL` is kept as the fallback default for `CPU_PROCESSOR_URL` / `GPU_PROCESSOR_URL`; it is removed in the Python service split plan once the URLs genuinely diverge.

## 7. Testing strategy

### Go

`backend/services/processor_test.go`:
- `httptest.Server` standing in for Python; asserts request body has `input_url` / `output_url` fields (string-matched against the URL the test fed in).
- Per-client timeout test: client constructed with 1ms timeout against a slow handler returns deadline-exceeded.
- One round-trip test per method, plus error cases (5xx upstream → wrapped error).

`backend/services/storage_test.go`, `storage_r2_test.go`:
- `Verify` returns nil on existing object, `ErrObjectNotMaterialized` on missing or zero-size.

Handler/job_runner tests use fakes implementing the two new client interfaces. Test doubles assert `SignGet/SignPut` was called on the expected key and the URL it returned was passed to the client.

### Python

`audio-processor/tests/test_io_url.py`:
- `respx` mock for `httpx.AsyncClient.get` / `put` — success returns tempfile; 4xx/5xx raises `HTTPException(502)`; network timeout same.

Per-router tests:
- Reuse existing `respx` patterns. Each test mocks the input GET, the upload PUT, and asserts the service code ran with the temp file path.

### End-to-end

Manual smoke after both halves merge, on `STORAGE_BACKEND=local`:

```bash
curl -s "localhost:8080/api/songs/search?q=hello+adele" | jq '.[0]'
# extract videoId, sig, then:
curl -s "localhost:8080/api/preview/$VIDEO_ID?sig=$SIG"
curl -s -X POST localhost:8080/api/preview-shift \
  -d "{\"video_id\":\"$VIDEO_ID\",\"sig\":\"$SIG\",\"semitones\":-3}"
curl -s -X POST localhost:8080/api/generate \
  -d "{\"video_id\":\"$VIDEO_ID\",\"sig\":\"$SIG\",\"semitones\":-3}"
# follow SSE to done, then:
curl -s "localhost:8080/api/audio/$VIDEO_ID/-3?sig=$SIG" -o /tmp/out.mp3
curl -s "localhost:8080/api/melody/$VIDEO_ID/-3?sig=$SIG"
```

R2 smoke deferred until the bucket exists in the infra setup plan.

## 8. Migration approach

Atomic per design Q2. Single plan, breaking change. No `/v2` endpoints. Each task is TDD'd; both Go and Python test suites pass before the next task. The final task does the cross-cutting E2E smoke and removes the `FilesystemPathForLocalProcessor` escape hatch.

Because Python and Go are not independently deployed during the dev/beta phase (no live infra yet), atomic break-and-replace is safe — they always run together from the same checkout.

## 9. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Python returns 200 OK without actually uploading | `storage.Verify` after every Python call. One HEAD per upload (~negligible cost). |
| Streamed PUT to R2 fails midway on large stems | Stems are ≤30MB. Standard PUT (not multipart) handles this. If we hit this in practice, switch to multipart later. |
| Presigned URL expires mid-processing on slow GPU work | Default TTL 600s (10 min) from Plan #1; Demucs caps at ~180s. 3× margin. Bump to 900s if any flake observed. |
| Python's existing pytest fixtures heavily encode old path-based schema | Update fixtures alongside routers; respx makes the URL mocking straightforward. |
| `*LocalDiskStorage` type assertions are scattered across 9 call sites | All 9 are removed in this plan — grep + compile failures act as a checklist. |

## 10. Out of scope (explicitly deferred)

- Search in `CPUProcessorClient` (next plan).
- Python service split into CPU + GPU services (next plan).
- Multipart R2 upload.
- Retries on transient R2/network errors (per Q3).
- Production-grade observability for the upload/download legs.
