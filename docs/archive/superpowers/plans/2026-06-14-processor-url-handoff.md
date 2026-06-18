# Plan #2: Processor URL Handoff + ProcessorClient CPU/GPU Split

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the path-based `ProcessorClient` with two URL-based interfaces (`CPUProcessorClient`, `GPUProcessorClient`). Python receives presigned URLs, downloads input → processes → PUTs output. Remove `FilesystemPathForLocalProcessor`. `STORAGE_BACKEND=r2` works end-to-end.

**Architecture:** Go mints input GET + output PUT URLs via `Storage.SignGet`/`SignPut`, sends both in the request body. Python uses a shared `_io_url` helper to stream-download, process locally, stream-upload, fail loudly with 502 on any HTTP error. Go verifies the object materialized via the new `Storage.Verify`. Two HTTP clients on the Go side with different timeouts (CPU 30s, GPU 180s), both pointed at the same Python service during this plan; the Python service split is the next plan.

**Tech Stack:** Go 1.22 + chi, AWS SDK v2 (existing `R2Storage`), FastAPI + Pydantic, httpx (already a dep), pytest.

**Spec:** `docs/superpowers/specs/2026-06-14-processor-url-handoff-design.md`.

---

## Reference: Target Go shapes

```go
// backend/services/storage.go (addition)

var ErrObjectNotMaterialized = errors.New("storage: object not materialized")

type Storage interface {
    // ... existing methods ...
    Verify(ctx context.Context, key string) error
}
```

```go
// backend/services/processor.go (replacement)

type CPUProcessorClient interface {
    Shift(ctx context.Context, inputURL, outputURL string, semitones float64) error
    PreviewKey(ctx context.Context, inputURL string) (string, error)
}

type GPUProcessorClient interface {
    Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error
    Melody(ctx context.Context, vocalsInputURL, outputURL string) error
}
```

## Reference: Target Python request bodies

| Endpoint | Body | Response |
|---|---|---|
| `POST /shift` | `{input_url, output_url, semitones}` | `{}` (200) |
| `POST /separate` | `{input_url, vocals_output_url, no_vocals_output_url}` | `{}` (204) |
| `POST /melody` | `{vocals_input_url, output_url}` | `{}` (200) |
| `POST /preview-key` | `{input_url}` | `{key}` (200) |

---

## Task 1: Add `Storage.Verify` + `ErrObjectNotMaterialized`

**Goal:** Add the post-upload verification helper to the interface and both impls. Cheap (one HEAD/stat per call) — wraps existing `Has`.

**Files:**
- Modify: `backend/services/storage.go`
- Modify: `backend/services/storage_r2.go`
- Modify: `backend/services/storage_test.go`
- Modify: `backend/services/storage_r2_test.go`

- [ ] **Step 1: Write failing tests in `storage_test.go`**

Append to the bottom of `backend/services/storage_test.go`:

```go
func TestLocalDiskStorage_Verify(t *testing.T) {
    s, err := services.NewLocalDiskStorage(t.TempDir())
    if err != nil {
        t.Fatalf("NewLocalDiskStorage: %v", err)
    }
    ctx := context.Background()

    cases := []struct {
        name      string
        setup     func(t *testing.T, key string)
        wantErrIs error
    }{
        {
            name:      "missing object → ErrObjectNotMaterialized",
            setup:     func(t *testing.T, key string) {},
            wantErrIs: services.ErrObjectNotMaterialized,
        },
        {
            name: "zero-byte object → ErrObjectNotMaterialized",
            setup: func(t *testing.T, key string) {
                t.Helper()
                src := filepath.Join(t.TempDir(), "empty")
                if err := os.WriteFile(src, []byte{}, 0o644); err != nil {
                    t.Fatalf("WriteFile: %v", err)
                }
                if err := s.Commit(ctx, key, src); err != nil {
                    t.Fatalf("Commit: %v", err)
                }
            },
            wantErrIs: services.ErrObjectNotMaterialized,
        },
        {
            name: "non-empty object → nil",
            setup: func(t *testing.T, key string) {
                t.Helper()
                src := filepath.Join(t.TempDir(), "ok")
                if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
                    t.Fatalf("WriteFile: %v", err)
                }
                if err := s.Commit(ctx, key, src); err != nil {
                    t.Fatalf("Commit: %v", err)
                }
            },
            wantErrIs: nil,
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            key := s.Key("abc12345678", "verify-"+tc.name+".bin")
            tc.setup(t, key)
            err := s.Verify(ctx, key)
            if tc.wantErrIs == nil {
                if err != nil {
                    t.Fatalf("Verify: got %v, want nil", err)
                }
                return
            }
            if !errors.Is(err, tc.wantErrIs) {
                t.Fatalf("Verify: got %v, want errors.Is(%v)", err, tc.wantErrIs)
            }
        })
    }
}
```

Add `"errors"` import if missing.

- [ ] **Step 2: Run tests, expect failure**

```bash
cd backend && go test ./services/ -run TestLocalDiskStorage_Verify -v
```

Expected: build error (`Verify` undefined, `ErrObjectNotMaterialized` undefined).

- [ ] **Step 3: Add `Verify` to the interface, `ErrObjectNotMaterialized`, and `LocalDiskStorage.Verify`**

In `backend/services/storage.go`, add (after the imports):

```go
// ErrObjectNotMaterialized is returned by Verify when the object is absent
// or zero-byte. Used after a Python upload to confirm the PUT actually
// landed before treating the cache entry as ready.
var ErrObjectNotMaterialized = errors.New("storage: object not materialized")
```

Add `"errors"` import.

Add to the `Storage` interface:

```go
Verify(ctx context.Context, key string) error
```

Add the method on `LocalDiskStorage` (after `Open`):

```go
// Verify reports nil if the object exists with non-zero size, otherwise
// ErrObjectNotMaterialized. Used after a Python upload completes (200 OK
// from the processor) to catch the "service returned success but the PUT
// silently failed" failure mode.
func (s *LocalDiskStorage) Verify(ctx context.Context, key string) error {
    ok, err := s.Has(ctx, key)
    if err != nil {
        return fmt.Errorf("storage: Verify %s: %w", key, err)
    }
    if !ok {
        return fmt.Errorf("storage: %s: %w", key, ErrObjectNotMaterialized)
    }
    return nil
}
```

- [ ] **Step 4: Add `R2Storage.Verify`**

In `backend/services/storage_r2.go` add:

```go
func (s *R2Storage) Verify(ctx context.Context, key string) error {
    ok, err := s.Has(ctx, key)
    if err != nil {
        return fmt.Errorf("storage(r2): Verify %s: %w", key, err)
    }
    if !ok {
        return fmt.Errorf("storage(r2): %s: %w", key, ErrObjectNotMaterialized)
    }
    return nil
}
```

- [ ] **Step 5: Write failing R2 test**

Append to `backend/services/storage_r2_test.go`:

```go
func TestR2Storage_Verify(t *testing.T) {
    cases := []struct {
        name      string
        handler   http.HandlerFunc
        wantErrIs error
    }{
        {
            name: "non-empty HEAD 200 → nil",
            handler: func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Length", "42")
                w.WriteHeader(http.StatusOK)
            },
            wantErrIs: nil,
        },
        {
            name: "zero-size HEAD 200 → ErrObjectNotMaterialized",
            handler: func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Length", "0")
                w.WriteHeader(http.StatusOK)
            },
            wantErrIs: services.ErrObjectNotMaterialized,
        },
        {
            name: "HEAD 404 → ErrObjectNotMaterialized",
            handler: func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(http.StatusNotFound)
            },
            wantErrIs: services.ErrObjectNotMaterialized,
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            srv := httptest.NewServer(tc.handler)
            defer srv.Close()
            s, err := services.NewR2Storage(services.R2Config{
                AccountID: "acct", AccessKeyID: "k", SecretAccessKey: "s",
                Bucket: "cantus-cache", PresignTTL: time.Minute, Endpoint: srv.URL,
            })
            if err != nil {
                t.Fatalf("NewR2Storage: %v", err)
            }
            err = s.Verify(context.Background(), "abc/melody.json")
            if tc.wantErrIs == nil {
                if err != nil {
                    t.Fatalf("Verify: got %v, want nil", err)
                }
                return
            }
            if !errors.Is(err, tc.wantErrIs) {
                t.Fatalf("Verify: got %v, want errors.Is(%v)", err, tc.wantErrIs)
            }
        })
    }
}
```

- [ ] **Step 6: Run all storage tests**

```bash
cd backend && go test ./services/ -run "TestLocalDiskStorage_Verify|TestR2Storage_Verify" -v
```

Expected: PASS.

- [ ] **Step 7: Build all packages to catch interface-implementation gaps**

```bash
cd backend && go build ./...
```

Expected: PASS. If a test double anywhere implements `Storage` and doesn't yet have `Verify`, add a no-op `Verify(context.Context, string) error { return nil }` to it. Fakes used today: search `_test.go` files for `Has(ctx context.Context` to find them.

- [ ] **Step 8: Run the full test suite**

```bash
cd backend && go test ./...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/services/
git commit -m "feat(storage): add Storage.Verify + ErrObjectNotMaterialized"
```

---

## Task 2: Add `CPU_PROCESSOR_URL` and `GPU_PROCESSOR_URL` config

**Goal:** New env vars, both default to existing `PYTHON_PROCESSOR_URL` so dev/prod behave identically until the Python split plan splits them. Also adds `CPU_PROCESSOR_TIMEOUT_SECONDS` (default 30) and `GPU_PROCESSOR_TIMEOUT_SECONDS` (default 180).

**Files:**
- Modify: `backend/config/config.go`
- Modify: `backend/config/config_test.go`
- Modify: `backend/.env.example`

- [ ] **Step 1: Write failing tests**

Append to `backend/config/config_test.go`:

```go
func TestLoad_processorURLs_defaultToPythonProcessorURL(t *testing.T) {
    t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))
    t.Setenv("PYTHON_PROCESSOR_URL", "http://py:9000")
    cfg, err := Load()
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if cfg.CPUProcessorURL != "http://py:9000" {
        t.Errorf("CPUProcessorURL: got %q, want %q", cfg.CPUProcessorURL, "http://py:9000")
    }
    if cfg.GPUProcessorURL != "http://py:9000" {
        t.Errorf("GPUProcessorURL: got %q, want %q", cfg.GPUProcessorURL, "http://py:9000")
    }
}

func TestLoad_processorURLs_overrideIndependently(t *testing.T) {
    t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))
    t.Setenv("PYTHON_PROCESSOR_URL", "http://py:9000")
    t.Setenv("CPU_PROCESSOR_URL", "http://cpu:8091")
    t.Setenv("GPU_PROCESSOR_URL", "http://gpu:8092")
    cfg, err := Load()
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if cfg.CPUProcessorURL != "http://cpu:8091" {
        t.Errorf("CPUProcessorURL: got %q", cfg.CPUProcessorURL)
    }
    if cfg.GPUProcessorURL != "http://gpu:8092" {
        t.Errorf("GPUProcessorURL: got %q", cfg.GPUProcessorURL)
    }
}

func TestLoad_processorTimeouts_defaults(t *testing.T) {
    t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))
    cfg, err := Load()
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if cfg.CPUProcessorTimeoutSeconds != 30 {
        t.Errorf("CPUProcessorTimeoutSeconds: got %d, want 30", cfg.CPUProcessorTimeoutSeconds)
    }
    if cfg.GPUProcessorTimeoutSeconds != 180 {
        t.Errorf("GPUProcessorTimeoutSeconds: got %d, want 180", cfg.GPUProcessorTimeoutSeconds)
    }
}
```

- [ ] **Step 2: Run tests, expect failure**

```bash
cd backend && go test ./config/ -run "TestLoad_processorURLs|TestLoad_processorTimeouts" -v
```

Expected: build error (fields undefined).

- [ ] **Step 3: Add fields and load logic**

In `backend/config/config.go`:

Add to the `Config` struct (after `PythonProcessorURL`):

```go
CPUProcessorURL            string // CPU_PROCESSOR_URL, default = PYTHON_PROCESSOR_URL
GPUProcessorURL            string // GPU_PROCESSOR_URL, default = PYTHON_PROCESSOR_URL
CPUProcessorTimeoutSeconds int    // CPU_PROCESSOR_TIMEOUT_SECONDS, default 30
GPUProcessorTimeoutSeconds int    // GPU_PROCESSOR_TIMEOUT_SECONDS, default 180
```

In `Load()`, after the existing `cfg.PythonProcessorURL = ...` line, add:

```go
cfg.CPUProcessorURL = getEnvString("CPU_PROCESSOR_URL", cfg.PythonProcessorURL)
cfg.GPUProcessorURL = getEnvString("GPU_PROCESSOR_URL", cfg.PythonProcessorURL)
if cfg.CPUProcessorTimeoutSeconds, err = getEnvInt("CPU_PROCESSOR_TIMEOUT_SECONDS", 30); err != nil {
    return nil, err
}
if cfg.GPUProcessorTimeoutSeconds, err = getEnvInt("GPU_PROCESSOR_TIMEOUT_SECONDS", 180); err != nil {
    return nil, err
}
```

- [ ] **Step 4: Run tests**

```bash
cd backend && go test ./config/ -v
```

Expected: PASS.

- [ ] **Step 5: Update `.env.example`**

Append to `backend/.env.example`:

```
# Processor URLs — per-client overrides (used after the Python service split).
# Both default to PYTHON_PROCESSOR_URL while a single Python service handles both.
# CPU_PROCESSOR_URL=
# GPU_PROCESSOR_URL=
# CPU_PROCESSOR_TIMEOUT_SECONDS=30
# GPU_PROCESSOR_TIMEOUT_SECONDS=180
```

- [ ] **Step 6: Commit**

```bash
git add backend/config/ backend/.env.example
git commit -m "config: add CPU/GPU processor URLs and timeouts"
```

---

## Task 3: Add Python shared `_io_url` helpers

**Goal:** Two async helpers used by all four routers — `download_to_temp(url, scratch) -> Path` and `upload_from_path(path, url) -> None`. Both surface HTTP/network failure as `HTTPException(502)`. Tests exercise each via a stubbed httpx client.

**Files:**
- Create: `audio-processor/routers/_io_url.py`
- Create: `audio-processor/tests/test_io_url.py`

- [ ] **Step 1: Write failing tests**

```python
# audio-processor/tests/test_io_url.py
from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Any

import httpx
import pytest
from fastapi import HTTPException

from routers._io_url import download_to_temp, upload_from_path


def _run(coro):
    return asyncio.run(coro)


class _FakeStreamResponse:
    def __init__(self, status_code: int, chunks: list[bytes]) -> None:
        self.status_code = status_code
        self._chunks = chunks

    async def aiter_bytes(self):
        for c in self._chunks:
            yield c

    def raise_for_status(self) -> None:
        if self.status_code >= 400:
            raise httpx.HTTPStatusError(
                "boom",
                request=httpx.Request("GET", "http://test"),
                response=httpx.Response(self.status_code),
            )


class _FakeClient:
    """Minimal stand-in for httpx.AsyncClient — supports stream() context manager
    and put(), records last call for assertions."""

    def __init__(
        self,
        *,
        get_response: _FakeStreamResponse | None = None,
        put_status: int = 200,
        raise_on: str | None = None,  # "get" or "put"
    ) -> None:
        self.get_response = get_response or _FakeStreamResponse(200, [b"hello"])
        self.put_status = put_status
        self.raise_on = raise_on
        self.last_put_body: bytes | None = None

    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc) -> None:
        return None

    def stream(self, method: str, url: str):
        outer = self

        class _Ctx:
            async def __aenter__(self_inner):
                if outer.raise_on == "get":
                    raise httpx.ConnectError("network down")
                return outer.get_response

            async def __aexit__(self_inner, *exc):
                return None

        return _Ctx()

    async def put(self, url: str, content: Any) -> httpx.Response:
        if self.raise_on == "put":
            raise httpx.ConnectError("network down")
        # `content` is an async iterator of bytes for streaming PUT.
        body = b""
        async for chunk in content:
            body += chunk
        self.last_put_body = body
        return httpx.Response(self.put_status)


@pytest.fixture
def scratch(tmp_path: Path) -> Path:
    return tmp_path


def test_download_to_temp_writes_streamed_bytes(scratch, monkeypatch):
    fake = _FakeClient(get_response=_FakeStreamResponse(200, [b"abc", b"def"]))
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    out = _run(download_to_temp("http://test/file", scratch))

    assert out.parent == scratch
    assert out.read_bytes() == b"abcdef"


def test_download_to_temp_http_error_raises_502(scratch, monkeypatch):
    fake = _FakeClient(get_response=_FakeStreamResponse(404, []))
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(download_to_temp("http://test/missing", scratch))
    assert ei.value.status_code == 502


def test_download_to_temp_network_error_raises_502(scratch, monkeypatch):
    fake = _FakeClient(raise_on="get")
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(download_to_temp("http://test/file", scratch))
    assert ei.value.status_code == 502


def test_upload_from_path_streams_file(scratch, monkeypatch):
    src = scratch / "out.bin"
    src.write_bytes(b"payload")
    fake = _FakeClient(put_status=200)
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    _run(upload_from_path(src, "http://test/dest"))

    assert fake.last_put_body == b"payload"


def test_upload_from_path_http_error_raises_502(scratch, monkeypatch):
    src = scratch / "out.bin"
    src.write_bytes(b"payload")
    fake = _FakeClient(put_status=500)
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(upload_from_path(src, "http://test/dest"))
    assert ei.value.status_code == 502


def test_upload_from_path_network_error_raises_502(scratch, monkeypatch):
    src = scratch / "out.bin"
    src.write_bytes(b"payload")
    fake = _FakeClient(raise_on="put")
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(upload_from_path(src, "http://test/dest"))
    assert ei.value.status_code == 502
```

- [ ] **Step 2: Run tests, expect failure (module missing)**

```bash
cd audio-processor && python -m pytest tests/test_io_url.py -v
```

Expected: `ModuleNotFoundError: routers._io_url`.

- [ ] **Step 3: Implement helpers**

```python
# audio-processor/routers/_io_url.py
"""HTTP-streamed input download and output upload helpers used by all
processor routers. Failures surface as HTTPException(502) so the Go caller
sees a Bad Gateway and treats the job as failed without retries."""

from __future__ import annotations

import uuid
from pathlib import Path

import httpx
from fastapi import HTTPException

# 60s covers a worst-case slow PUT of a ~30MB stem on a flaky connection.
# Demucs/CREPE wall-clock is bounded separately by the GPU 180s timeout.
_HTTP_TIMEOUT = httpx.Timeout(60.0)


def _client() -> httpx.AsyncClient:
    """Indirection so tests can monkeypatch a fake client."""
    return httpx.AsyncClient(timeout=_HTTP_TIMEOUT)


async def download_to_temp(url: str, scratch: Path) -> Path:
    """Stream-GET `url` into a freshly-named file under `scratch`. Returns the
    written path. Raises HTTPException(502) on any HTTP or network error."""
    dst = scratch / f"in-{uuid.uuid4().hex}.bin"
    try:
        async with _client() as client:
            async with client.stream("GET", url) as resp:
                resp.raise_for_status()
                with dst.open("wb") as f:
                    async for chunk in resp.aiter_bytes():
                        f.write(chunk)
    except (httpx.HTTPError, httpx.HTTPStatusError) as exc:
        raise HTTPException(status_code=502, detail=f"download failed: {exc}") from exc
    return dst


async def upload_from_path(path: Path, url: str) -> None:
    """Stream-PUT `path` to `url`. Raises HTTPException(502) on failure."""

    async def _iter_file():
        with path.open("rb") as f:
            while True:
                chunk = f.read(64 * 1024)
                if not chunk:
                    break
                yield chunk

    try:
        async with _client() as client:
            resp = await client.put(url, content=_iter_file())
            if resp.status_code >= 400:
                raise HTTPException(
                    status_code=502,
                    detail=f"upload failed: status {resp.status_code}",
                )
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"upload failed: {exc}") from exc
```

- [ ] **Step 4: Run tests**

```bash
cd audio-processor && python -m pytest tests/test_io_url.py -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add audio-processor/routers/_io_url.py audio-processor/tests/test_io_url.py
git commit -m "feat(audio): add shared httpx download/upload helpers"
```

---

## Task 4: Create new `CPUProcessorClient` / `GPUProcessorClient` (Go side, ahead of Python migration)

**Goal:** Build the two URL-based Go clients (interfaces + concrete `PythonCPUProcessorClient` / `PythonGPUProcessorClient`) with all four methods. Unit tests use `httptest.Server` so they pass even though the real Python service still speaks the old protocol. The old combined `ProcessorClient` and `PythonProcessorClient` are untouched and continue to back live handler calls. Wire the two new clients in `main.go` alongside the old one.

**Files:**
- Create: `backend/services/processor_url.go` (new file for new clients to keep diffs scoped)
- Create: `backend/services/processor_url_test.go`
- Modify: `backend/cmd/server/main.go` (instantiate but don't use yet)

- [ ] **Step 1: Write failing tests in `processor_url_test.go`**

```go
package services_test

import (
    "context"
    "encoding/json"
    "errors"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "cantus/backend/services"
)

func TestPythonCPUProcessorClient_Shift_sendsURLBody(t *testing.T) {
    var gotPath, gotInputURL, gotOutputURL string
    var gotSemitones float64
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        var body struct {
            InputURL  string  `json:"input_url"`
            OutputURL string  `json:"output_url"`
            Semitones float64 `json:"semitones"`
        }
        if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
            t.Errorf("decode: %v", err)
        }
        gotInputURL = body.InputURL
        gotOutputURL = body.OutputURL
        gotSemitones = body.Semitones
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{}`))
    }))
    defer srv.Close()

    c := services.NewPythonCPUProcessorClient(srv.URL, &http.Client{Timeout: 5 * time.Second})
    if err := c.Shift(context.Background(), "https://r2/in.wav", "https://r2/out.mp3", -3); err != nil {
        t.Fatalf("Shift: %v", err)
    }
    if gotPath != "/shift" {
        t.Errorf("path: got %q, want /shift", gotPath)
    }
    if gotInputURL != "https://r2/in.wav" {
        t.Errorf("input_url: got %q", gotInputURL)
    }
    if gotOutputURL != "https://r2/out.mp3" {
        t.Errorf("output_url: got %q", gotOutputURL)
    }
    if gotSemitones != -3 {
        t.Errorf("semitones: got %v", gotSemitones)
    }
}

func TestPythonCPUProcessorClient_Shift_upstreamErrorReturnsErr(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        http.Error(w, "boom", http.StatusBadGateway)
    }))
    defer srv.Close()
    c := services.NewPythonCPUProcessorClient(srv.URL, &http.Client{Timeout: 5 * time.Second})
    err := c.Shift(context.Background(), "u", "v", 0)
    if err == nil || !strings.Contains(err.Error(), "502") {
        t.Fatalf("Shift err: got %v, want containing 502", err)
    }
}

func TestPythonCPUProcessorClient_PreviewKey_returnsKey(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/preview-key" {
            t.Errorf("path: %q", r.URL.Path)
        }
        var body struct {
            InputURL string `json:"input_url"`
        }
        _ = json.NewDecoder(r.Body).Decode(&body)
        if body.InputURL != "https://r2/in.mp3" {
            t.Errorf("input_url: %q", body.InputURL)
        }
        _, _ = w.Write([]byte(`{"key":"A minor"}`))
    }))
    defer srv.Close()
    c := services.NewPythonCPUProcessorClient(srv.URL, &http.Client{Timeout: time.Second})
    key, err := c.PreviewKey(context.Background(), "https://r2/in.mp3")
    if err != nil {
        t.Fatalf("PreviewKey: %v", err)
    }
    if key != "A minor" {
        t.Errorf("key: got %q, want A minor", key)
    }
}

func TestPythonGPUProcessorClient_Separate_sendsBothPutURLs(t *testing.T) {
    var body struct {
        InputURL          string `json:"input_url"`
        VocalsOutputURL   string `json:"vocals_output_url"`
        NoVocalsOutputURL string `json:"no_vocals_output_url"`
    }
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/separate" {
            t.Errorf("path: %q", r.URL.Path)
        }
        _ = json.NewDecoder(r.Body).Decode(&body)
        w.WriteHeader(http.StatusNoContent)
    }))
    defer srv.Close()
    c := services.NewPythonGPUProcessorClient(srv.URL, &http.Client{Timeout: 5 * time.Second})
    err := c.Separate(context.Background(), "https://r2/in.mp3", "https://r2/v.wav", "https://r2/nv.wav")
    if err != nil {
        t.Fatalf("Separate: %v", err)
    }
    if body.InputURL != "https://r2/in.mp3" || body.VocalsOutputURL != "https://r2/v.wav" || body.NoVocalsOutputURL != "https://r2/nv.wav" {
        t.Errorf("body: %+v", body)
    }
}

func TestPythonGPUProcessorClient_Melody_sendsBody(t *testing.T) {
    var body struct {
        VocalsInputURL string `json:"vocals_input_url"`
        OutputURL      string `json:"output_url"`
    }
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/melody" {
            t.Errorf("path: %q", r.URL.Path)
        }
        _ = json.NewDecoder(r.Body).Decode(&body)
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{}`))
    }))
    defer srv.Close()
    c := services.NewPythonGPUProcessorClient(srv.URL, &http.Client{Timeout: 5 * time.Second})
    if err := c.Melody(context.Background(), "https://r2/v.wav", "https://r2/m.json"); err != nil {
        t.Fatalf("Melody: %v", err)
    }
    if body.VocalsInputURL != "https://r2/v.wav" || body.OutputURL != "https://r2/m.json" {
        t.Errorf("body: %+v", body)
    }
}

func TestPythonCPUProcessorClient_Shift_contextCanceled(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        time.Sleep(50 * time.Millisecond)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()
    c := services.NewPythonCPUProcessorClient(srv.URL, &http.Client{Timeout: time.Hour})
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    err := c.Shift(ctx, "u", "v", 0)
    if err == nil || !errors.Is(err, context.Canceled) {
        t.Fatalf("err: got %v, want errors.Is(context.Canceled)", err)
    }
}
```

- [ ] **Step 2: Run tests, expect compile failure**

```bash
cd backend && go test ./services/ -run "PythonCPUProcessorClient|PythonGPUProcessorClient" -v
```

Expected: undefined identifiers.

- [ ] **Step 3: Implement the new clients**

Create `backend/services/processor_url.go`:

```go
package services

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
)

// CPUProcessorClient covers Python work that does not require a GPU.
type CPUProcessorClient interface {
    Shift(ctx context.Context, inputURL, outputURL string, semitones float64) error
    PreviewKey(ctx context.Context, inputURL string) (string, error)
}

// GPUProcessorClient covers Python work that needs a GPU.
type GPUProcessorClient interface {
    Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error
    Melody(ctx context.Context, vocalsInputURL, outputURL string) error
}

// PythonCPUProcessorClient is the HTTP-backed CPUProcessorClient.
type PythonCPUProcessorClient struct {
    baseURL string
    client  *http.Client
}

// NewPythonCPUProcessorClient returns a CPU client. http.Client.Timeout governs
// the overall request budget (~30s in production).
func NewPythonCPUProcessorClient(baseURL string, client *http.Client) *PythonCPUProcessorClient {
    return &PythonCPUProcessorClient{baseURL: baseURL, client: client}
}

// PythonGPUProcessorClient is the HTTP-backed GPUProcessorClient.
type PythonGPUProcessorClient struct {
    baseURL string
    client  *http.Client
}

// NewPythonGPUProcessorClient returns a GPU client. http.Client.Timeout should
// be large enough to absorb HF Space cold start + Demucs runtime (~180s).
func NewPythonGPUProcessorClient(baseURL string, client *http.Client) *PythonGPUProcessorClient {
    return &PythonGPUProcessorClient{baseURL: baseURL, client: client}
}

// postJSON marshals body, POSTs to baseURL+path, and decodes the response into
// out (which may be nil to discard). 2xx success.
func postJSON(ctx context.Context, client *http.Client, baseURL, path string, body, out any) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    buf, err := json.Marshal(body)
    if err != nil {
        return fmt.Errorf("processor %s: marshal: %w", path, err)
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(buf))
    if err != nil {
        return fmt.Errorf("processor %s: build request: %w", path, err)
    }
    req.Header.Set("Content-Type", "application/json")
    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("processor %s: do: %w", path, err)
    }
    defer func() { _ = resp.Body.Close() }()
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return fmt.Errorf("processor %s: upstream status %d", path, resp.StatusCode)
    }
    if out == nil {
        return nil
    }
    if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
        return fmt.Errorf("processor %s: decode: %w", path, err)
    }
    return nil
}

func (p *PythonCPUProcessorClient) Shift(ctx context.Context, inputURL, outputURL string, semitones float64) error {
    return postJSON(ctx, p.client, p.baseURL, "/shift", map[string]any{
        "input_url":  inputURL,
        "output_url": outputURL,
        "semitones":  semitones,
    }, nil)
}

func (p *PythonCPUProcessorClient) PreviewKey(ctx context.Context, inputURL string) (string, error) {
    var resp struct {
        Key string `json:"key"`
    }
    if err := postJSON(ctx, p.client, p.baseURL, "/preview-key", map[string]any{
        "input_url": inputURL,
    }, &resp); err != nil {
        return "", err
    }
    return resp.Key, nil
}

func (p *PythonGPUProcessorClient) Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error {
    return postJSON(ctx, p.client, p.baseURL, "/separate", map[string]any{
        "input_url":            inputURL,
        "vocals_output_url":    vocalsOutputURL,
        "no_vocals_output_url": noVocalsOutputURL,
    }, nil)
}

func (p *PythonGPUProcessorClient) Melody(ctx context.Context, vocalsInputURL, outputURL string) error {
    return postJSON(ctx, p.client, p.baseURL, "/melody", map[string]any{
        "vocals_input_url": vocalsInputURL,
        "output_url":       outputURL,
    }, nil)
}
```

- [ ] **Step 4: Run new tests**

```bash
cd backend && go test ./services/ -run "PythonCPUProcessorClient|PythonGPUProcessorClient" -v
```

Expected: PASS. All 6 tests green.

- [ ] **Step 5: Instantiate (but don't yet use) in `main.go`**

In `backend/cmd/server/main.go`, after the existing `processor := services.NewPythonProcessorClient(...)` line, add:

```go
cpuProc := services.NewPythonCPUProcessorClient(
    cfg.CPUProcessorURL,
    &http.Client{Timeout: time.Duration(cfg.CPUProcessorTimeoutSeconds) * time.Second},
)
gpuProc := services.NewPythonGPUProcessorClient(
    cfg.GPUProcessorURL,
    &http.Client{Timeout: time.Duration(cfg.GPUProcessorTimeoutSeconds) * time.Second},
)
_ = cpuProc
_ = gpuProc
```

(The `_ =` silences "declared and not used" until Task 8 wires them through. They will be passed to `NewJobRunner` and `api.NewRouter` once the consumers exist.)

- [ ] **Step 6: Run full suite**

```bash
cd backend && go build ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/services/processor_url.go backend/services/processor_url_test.go backend/cmd/server/main.go
git commit -m "feat(processor): add URL-based CPU/GPU processor clients"
```

---

## Task 5: Migrate `/shift` end-to-end

**Goal:** Python `/shift` accepts URLs and uses `_io_url` helpers. Go callers in `preview_shift.go` and `job_runner.go` switch from `processor.Shift(path, path, ...)` to `cpuProc.Shift(url, url, ...)` + `storage.Verify`. The old `processor.Shift` method continues to exist (it's the old `ProcessorClient` interface — removed in Task 9) but no Go code calls it anymore.

**Files:**
- Modify: `audio-processor/routers/shift.py`
- Modify: `audio-processor/tests/test_shift_router.py`
- Modify: `backend/services/job_runner.go`
- Modify: `backend/services/job_runner_test.go`
- Modify: `backend/api/handlers/preview_shift.go`
- Modify: `backend/api/handlers/preview_shift_test.go`
- Modify: `backend/api/router.go`
- Modify: `backend/cmd/server/main.go`

### Python side

- [ ] **Step 1: Rewrite Python tests for the URL contract**

Replace the contents of `audio-processor/tests/test_shift_router.py` with:

```python
from __future__ import annotations

import asyncio
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from main import app
from routers import shift as shift_router
from routers.shift import get_pitch_service


class _StubPitchService:
    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str, float]] = []
        self._raise = raise_exc

    def shift(self, input_path: str, output_path: str, semitones: float) -> None:
        self.calls.append((input_path, output_path, semitones))
        if self._raise is not None:
            raise self._raise


@pytest.fixture
def stub_io(monkeypatch, tmp_path):
    """Stubs download_to_temp and upload_from_path on the shift router.
    Returns a dict tracking calls."""
    state = {"downloaded": None, "uploaded": None, "input_url": None, "output_url": None}

    async def fake_download(url: str, scratch: Path) -> Path:
        state["input_url"] = url
        p = scratch / "in.bin"
        p.write_bytes(b"audio-bytes")
        state["downloaded"] = p
        return p

    async def fake_upload(path: Path, url: str) -> None:
        state["output_url"] = url
        state["uploaded"] = path.read_bytes() if path.exists() else None

    monkeypatch.setattr(shift_router, "download_to_temp", fake_download)
    monkeypatch.setattr(shift_router, "upload_from_path", fake_upload)
    return state


@pytest.fixture
def client_with_stub(stub_io):
    stub = _StubPitchService()
    # Write a file to the output path the service "creates" so upload_from_path sees bytes.
    real_shift = stub.shift

    def shift_writing_output(input_path: str, output_path: str, semitones: float):
        real_shift(input_path, output_path, semitones)
        Path(output_path).write_bytes(b"shifted-bytes")

    stub.shift = shift_writing_output  # type: ignore[assignment]
    app.dependency_overrides[get_pitch_service] = lambda: stub
    try:
        yield TestClient(app), stub, stub_io
    finally:
        app.dependency_overrides.clear()


def test_shift_happy_path(client_with_stub):
    client, stub, io_state = client_with_stub
    body = {
        "input_url": "https://r2.test/in.wav",
        "output_url": "https://r2.test/out.mp3",
        "semitones": 2.0,
    }
    resp = client.post("/shift", json=body)
    assert resp.status_code == 200
    assert io_state["input_url"] == "https://r2.test/in.wav"
    assert io_state["output_url"] == "https://r2.test/out.mp3"
    assert io_state["uploaded"] == b"shifted-bytes"
    assert len(stub.calls) == 1
    assert stub.calls[0][2] == 2.0


@pytest.mark.parametrize("semitones", [13.0, -13.0])
def test_shift_semitones_out_of_range_returns_422(semitones, client_with_stub):
    client, stub, _ = client_with_stub
    resp = client.post("/shift", json={
        "input_url": "https://r2.test/in",
        "output_url": "https://r2.test/out",
        "semitones": semitones,
    })
    assert resp.status_code == 422
    assert stub.calls == []


@pytest.mark.parametrize(
    "body",
    [
        {"output_url": "u", "semitones": 0.0},
        {"input_url": "u", "semitones": 0.0},
        {"input_url": "", "output_url": "u", "semitones": 0.0},
        {"input_url": "u", "output_url": "", "semitones": 0.0},
    ],
    ids=["missing-input-url", "missing-output-url", "empty-input-url", "empty-output-url"],
)
def test_shift_missing_required_fields_returns_422(body, client_with_stub):
    client, stub, _ = client_with_stub
    resp = client.post("/shift", json=body)
    assert resp.status_code == 422
    assert stub.calls == []


def test_shift_service_runtime_error_returns_500(monkeypatch, stub_io):
    stub = _StubPitchService(raise_exc=RuntimeError("ffmpeg failed"))
    app.dependency_overrides[get_pitch_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post("/shift", json={
            "input_url": "u",
            "output_url": "v",
            "semitones": 0.0,
        })
        assert resp.status_code == 500
        assert "ffmpeg" in resp.json()["detail"]
    finally:
        app.dependency_overrides.clear()
```

- [ ] **Step 2: Run tests, expect failure**

```bash
cd audio-processor && python -m pytest tests/test_shift_router.py -v
```

Expected: failures (schema still uses `input_path`).

- [ ] **Step 3: Rewrite `routers/shift.py`**

```python
from __future__ import annotations

import asyncio
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp, upload_from_path
from services.pitch_service import PitchService


class ShiftRequest(BaseModel):
    input_url: str = Field(min_length=1)
    output_url: str = Field(min_length=1)
    semitones: float = Field(ge=-12, le=12)


@lru_cache(maxsize=1)
def get_pitch_service() -> PitchService:
    """Singleton PitchService. Tests override via app.dependency_overrides."""
    return PitchService()


ShiftServiceDep = Annotated[PitchService, Depends(get_pitch_service)]

router = APIRouter()


@router.post("/shift")
def shift(req: ShiftRequest, service: ShiftServiceDep) -> dict:
    """Download input_url → shift locally → upload to output_url. Synchronous
    HTTP handler that internally drives the async io helpers via asyncio.run."""

    async def _run() -> None:
        with tempfile.TemporaryDirectory(prefix="shift-") as td:
            scratch = Path(td)
            src = await download_to_temp(req.input_url, scratch)
            dst = scratch / "out.mp3"
            try:
                service.shift(str(src), str(dst), req.semitones)
            except RuntimeError as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc
            await upload_from_path(dst, req.output_url)

    asyncio.run(_run())
    return {}
```

- [ ] **Step 4: Run Python tests**

```bash
cd audio-processor && python -m pytest tests/test_shift_router.py -v
```

Expected: PASS.

### Go side

- [ ] **Step 5: Update `JobRunner` to take `cpu` and `gpu` clients, switch Shift call**

In `backend/services/job_runner.go`:

Replace the struct + constructor + the Shift call site:

```go
type JobRunner struct {
    ytSvc     YouTubeService
    storage   Storage
    cpu       CPUProcessorClient
    gpu       GPUProcessorClient
    processor ProcessorClient // TODO Task 9: remove with old ProcessorClient
    jobStore  *JobStore
    semaphore chan struct{}
    inflight  sync.Map
}

func NewJobRunner(
    ytSvc YouTubeService,
    storage Storage,
    cpu CPUProcessorClient,
    gpu GPUProcessorClient,
    processor ProcessorClient,
    jobStore *JobStore,
    maxConcurrent int,
) *JobRunner {
    if maxConcurrent < 1 {
        maxConcurrent = 1
    }
    return &JobRunner{
        ytSvc: ytSvc, storage: storage, cpu: cpu, gpu: gpu,
        processor: processor, jobStore: jobStore,
        semaphore: make(chan struct{}, maxConcurrent),
    }
}
```

Inside `Run`, replace the Stage 4 shift block (the one that today uses `localDisk.FilesystemPathForLocalProcessor` and `r.processor.Shift`) with:

```go
    // Stage 4: Pitch-shift the instrumental stem to the requested key.
    r.update(jobID, models.StatusShifting, "shifting instrumental to your key")
    shiftedName := "shifted/" + strconv.Itoa(semitones) + "/audio.mp3"
    shiftedKey := r.storage.Key(videoID, shiftedName)
    shiftedHas, _ := r.storage.Has(ctx, shiftedKey)
    if !shiftedHas {
        noVocalsKey := r.storage.Key(videoID, "no_vocals.wav")
        inURL, err := r.storage.SignGet(ctx, noVocalsKey)
        if err != nil {
            r.fail(jobID, "sign get failed: "+err.Error())
            return
        }
        outURL, err := r.storage.SignPut(ctx, shiftedKey)
        if err != nil {
            r.fail(jobID, "sign put failed: "+err.Error())
            return
        }
        if err := r.cpu.Shift(ctx, inURL, outURL, float64(semitones)); err != nil {
            r.fail(jobID, "shift failed: "+err.Error())
            return
        }
        if err := r.storage.Verify(ctx, shiftedKey); err != nil {
            r.fail(jobID, "shift output not materialized: "+err.Error())
            return
        }
    }
```

Remove the `tmpDir`/`tmpOut`/`storage.Commit` bookkeeping; Python uploads directly via PUT. Also remove the now-unused `"os"`, `"path/filepath"` imports if no other code in the file uses them — `go vet` will flag this.

- [ ] **Step 6: Update `job_runner_test.go`**

In `backend/services/job_runner_test.go`, find the `fakeProcessorJob` struct and add new fakes implementing the URL interfaces. Append:

```go
type fakeCPUJob struct {
    shiftFn func(ctx context.Context, in, out string, st float64) error
    calls   []struct{ In, Out string; Semi float64 }
}

func (f *fakeCPUJob) Shift(ctx context.Context, in, out string, st float64) error {
    f.calls = append(f.calls, struct{ In, Out string; Semi float64 }{in, out, st})
    if f.shiftFn != nil {
        return f.shiftFn(ctx, in, out, st)
    }
    return nil
}
func (f *fakeCPUJob) PreviewKey(context.Context, string) (string, error) { return "", nil }

type fakeGPUJob struct{}

func (f *fakeGPUJob) Separate(context.Context, string, string, string) error { return nil }
func (f *fakeGPUJob) Melody(context.Context, string, string) error           { return nil }
```

For each existing `NewJobRunner(...)` call site in the test file, insert `&fakeCPUJob{}, &fakeGPUJob{}` arguments before the existing processor arg.

Update any tests that asserted on filesystem paths or `Commit` calls for the shifted output: they now must seed the storage instead (write the shifted file directly into `storage` so `Verify` passes), and assert that the fake `Shift` was called with non-empty URLs. Pattern:

```go
// After NewJobRunner, before Submit, stage the shifted output to simulate Python upload.
cpu := &fakeCPUJob{shiftFn: func(ctx context.Context, in, out string, st float64) error {
    // Simulate Python: write a non-empty file at the expected storage key.
    key := storage.Key(videoID, "shifted/" + strconv.Itoa(semi) + "/audio.mp3")
    src := filepath.Join(t.TempDir(), "shifted.mp3")
    _ = os.WriteFile(src, []byte("MP3"), 0o644)
    return storage.Commit(context.Background(), key, src)
}}
```

(Same Commit shortcut used by previous tests; the production path doesn't need it because real Python uploads via the blob handler.)

- [ ] **Step 7: Update `PreviewShift` handler**

In `backend/api/handlers/preview_shift.go`, change the signature:

```go
func PreviewShift(
    signer *services.Signer,
    storage services.Storage,
    ytSvc services.YouTubeService,
    cpu services.CPUProcessorClient,
) http.HandlerFunc {
```

Replace **both** `Shift` blocks (the stem path around line 91-124 and the legacy fallback around line 156-187). Each `Shift` block becomes:

```go
    inKey := storage.Key(videoID, /* "preview-stems/no_vocals.wav" or "preview.mp3" */)
    outKey := storage.Key(videoID, /* stemShiftedName or legacyShiftedName */)
    inURL, err := storage.SignGet(ctx, inKey)
    if err != nil {
        log.Error().Err(err).Msg("storage.SignGet failed")
        writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign get failed"})
        return
    }
    outURL, err := storage.SignPut(ctx, outKey)
    if err != nil {
        log.Error().Err(err).Msg("storage.SignPut failed")
        writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign put failed"})
        return
    }
    if err := cpu.Shift(ctx, inURL, outURL, float64(n)); err != nil {
        log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("processor.Shift failed")
        writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
        return
    }
    if err := storage.Verify(ctx, outKey); err != nil {
        log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Verify failed")
        writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "shift output not materialized"})
        return
    }
    serveKey = outKey
    serveName = /* matching name string */
```

Delete the `local, ok := storage.(*services.LocalDiskStorage)` assertions, `os.MkdirTemp`/`os.RemoveAll` calls, `filepath.Join` for `outputPath`, and `storage.Commit` calls in this file. The `os`/`filepath` imports likely become unused — let `go vet` tell you.

- [ ] **Step 8: Update `preview_shift_test.go`**

Same pattern as Step 6: rename `fakeProcessor` to a CPU-only fake (or add a new one) implementing `services.CPUProcessorClient`. Pre-stage the shifted output inside the fake's `Shift` body so `Verify` succeeds. Update the handler construction call to pass the new arg.

- [ ] **Step 9: Update `api/router.go`**

In `backend/api/router.go`, change `NewRouter`'s signature to accept the two new clients (in addition to the existing `processor` for now — removed in Task 9):

```go
func NewRouter(
    allowedOrigins []string,
    log zerolog.Logger,
    svc services.YouTubeService,
    signer *services.Signer,
    storage services.Storage,
    processor services.ProcessorClient,   // legacy; removed in Task 9
    cpu services.CPUProcessorClient,
    gpu services.GPUProcessorClient,
    jobRunner services.JobSubmitter,
    jobStore *services.JobStore,
    blobTokener *services.BlobTokener,
) *chi.Mux {
```

Inside `NewRouter`, pass `cpu` to `handlers.PreviewShift` (was `processor`).

- [ ] **Step 10: Update `main.go`**

In `backend/cmd/server/main.go`, drop the `_ = cpuProc` / `_ = gpuProc` lines added in Task 4; pass them to `NewJobRunner` and `api.NewRouter`:

```go
jobRunner := services.NewJobRunner(svc, storage, cpuProc, gpuProc, processor, jobStore, maxJobs)
r := api.NewRouter(origins, log, svc, signer, storage, processor, cpuProc, gpuProc, jobRunner, jobStore, blobTokener)
```

- [ ] **Step 11: Build + test**

```bash
cd backend && go build ./... && go test ./...
```

Expected: PASS. Any handler/router test that constructs `NewRouter` directly needs its arg list updated to insert `cpu, gpu`.

- [ ] **Step 12: Local smoke (shift only)**

In two terminals, run audio-processor and backend, then:

```bash
# Use a videoId you already have stems for in tmp/cache/.
# Sign one via signing key as needed; quickest is to search first.
curl -s "localhost:8080/api/songs/search?q=hello+adele" | jq '.[0]'
# Pick videoId + sig, then:
curl -s -X POST localhost:8080/api/preview-shift \
  -H 'content-type: application/json' \
  -d '{"video_id":"<id>","sig":"<sig>","semitones":-3}' \
  -o /tmp/shifted.mp3 && file /tmp/shifted.mp3
```

Expected: an MP3 file (not an error JSON). If you get a 502, check audio-processor logs — the URL it received should be `http://localhost:8080/internal/blob/...`.

- [ ] **Step 13: Commit**

```bash
git add audio-processor/routers/shift.py audio-processor/tests/test_shift_router.py \
        backend/services/job_runner.go backend/services/job_runner_test.go \
        backend/api/handlers/preview_shift.go backend/api/handlers/preview_shift_test.go \
        backend/api/router.go backend/cmd/server/main.go
git commit -m "feat(processor): migrate /shift to URL handoff"
```

---

## Task 6: Migrate `/preview-key` end-to-end

**Goal:** Same protocol switch for `/preview-key`. Note: no current production code path calls `processor.PreviewKey` (the `PreviewKey` HTTP handler is now a melody.json reader). This task makes the new `CPUProcessorClient.PreviewKey` actually work end-to-end against the new Python contract so the next plan can rely on it when needed.

**Files:**
- Modify: `audio-processor/routers/preview_key.py`
- Modify: `audio-processor/tests/test_preview_key_router.py`

- [ ] **Step 1: Rewrite Python tests**

Replace `audio-processor/tests/test_preview_key_router.py`:

```python
from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from main import app
from routers import preview_key as pk_router
from routers.preview_key import get_preview_key_service


class _StubPreviewKeyService:
    def __init__(self, *, key: str = "A minor", raise_exc: Exception | None = None) -> None:
        self.calls: list[str] = []
        self._key = key
        self._raise = raise_exc

    def estimate(self, input_path: str) -> str:
        self.calls.append(input_path)
        if self._raise is not None:
            raise self._raise
        return self._key


@pytest.fixture
def stub_download(monkeypatch, tmp_path):
    state = {"url": None}

    async def fake_download(url: str, scratch: Path) -> Path:
        state["url"] = url
        p = scratch / "preview.bin"
        p.write_bytes(b"preview-bytes")
        return p

    monkeypatch.setattr(pk_router, "download_to_temp", fake_download)
    return state


def test_preview_key_happy_path(stub_download):
    stub = _StubPreviewKeyService(key="C major")
    app.dependency_overrides[get_preview_key_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post("/preview-key", json={"input_url": "https://r2.test/p.mp3"})
        assert resp.status_code == 200
        assert resp.json() == {"key": "C major"}
        assert stub_download["url"] == "https://r2.test/p.mp3"
        assert len(stub.calls) == 1
    finally:
        app.dependency_overrides.clear()


@pytest.mark.parametrize("body,ids", [
    ({}, "missing"),
    ({"input_url": ""}, "empty"),
])
def test_preview_key_invalid_body_returns_422(body, ids, stub_download):
    client = TestClient(app)
    resp = client.post("/preview-key", json=body)
    assert resp.status_code == 422


def test_preview_key_service_error_returns_500(stub_download):
    stub = _StubPreviewKeyService(raise_exc=RuntimeError("librosa boom"))
    app.dependency_overrides[get_preview_key_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post("/preview-key", json={"input_url": "u"})
        assert resp.status_code == 500
        assert "librosa" in resp.json()["detail"]
    finally:
        app.dependency_overrides.clear()
```

- [ ] **Step 2: Run, expect failure**

```bash
cd audio-processor && python -m pytest tests/test_preview_key_router.py -v
```

- [ ] **Step 3: Rewrite `routers/preview_key.py`**

```python
from __future__ import annotations

import asyncio
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp
from services.preview_key_service import PreviewKeyService


class PreviewKeyRequest(BaseModel):
    input_url: str = Field(min_length=1)


class PreviewKeyResponse(BaseModel):
    key: str


@lru_cache(maxsize=1)
def get_preview_key_service() -> PreviewKeyService:
    """Singleton PreviewKeyService. Tests override via app.dependency_overrides."""
    return PreviewKeyService()


PreviewKeyServiceDep = Annotated[PreviewKeyService, Depends(get_preview_key_service)]
router = APIRouter()


@router.post("/preview-key", response_model=PreviewKeyResponse)
def preview_key(req: PreviewKeyRequest, service: PreviewKeyServiceDep) -> PreviewKeyResponse:
    """Download input_url → estimate musical key."""

    async def _run() -> str:
        with tempfile.TemporaryDirectory(prefix="preview-key-") as td:
            src = await download_to_temp(req.input_url, Path(td))
            try:
                return service.estimate(str(src))
            except Exception as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc

    key = asyncio.run(_run())
    return PreviewKeyResponse(key=key)
```

- [ ] **Step 4: Tests pass**

```bash
cd audio-processor && python -m pytest tests/test_preview_key_router.py -v
```

Expected: PASS. (Go side has no callers to update — the `PreviewKey` HTTP handler reads `melody.json` and never calls `processor.PreviewKey`.)

- [ ] **Step 5: Commit**

```bash
git add audio-processor/routers/preview_key.py audio-processor/tests/test_preview_key_router.py
git commit -m "feat(processor): migrate /preview-key to URL handoff"
```

---

## Task 7: Migrate `/separate` end-to-end

**Goal:** Python `/separate` accepts `input_url + two PUT URLs`, uploads both stems directly. Go callers in `preview_stems.go` and `job_runner.go` switch to `gpu.Separate` and `Verify` both stems.

**Files:**
- Modify: `audio-processor/routers/separate.py`
- Modify: `audio-processor/tests/test_separate_router.py`
- Modify: `backend/services/job_runner.go`
- Modify: `backend/services/job_runner_test.go`
- Modify: `backend/api/handlers/preview_stems.go`
- Modify: `backend/api/handlers/preview_stems_test.go`
- Modify: `backend/api/router.go`
- Modify: `backend/cmd/server/main.go`

### Python side

- [ ] **Step 1: Rewrite Python tests**

Replace `audio-processor/tests/test_separate_router.py`:

```python
from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from main import app
from routers import separate as sep_router
from routers.separate import get_demucs_service


class _StubDemucs:
    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str]] = []
        self._raise = raise_exc

    def separate(self, input_path: str, output_dir: str) -> None:
        self.calls.append((input_path, output_dir))
        if self._raise is not None:
            raise self._raise
        # Simulate Demucs writing both stems into output_dir.
        Path(output_dir, "vocals.wav").write_bytes(b"VOCALS")
        Path(output_dir, "no_vocals.wav").write_bytes(b"NO-VOCALS")


@pytest.fixture
def stub_io(monkeypatch):
    state = {
        "input_url": None,
        "vocals_url": None,
        "no_vocals_url": None,
        "uploaded": {},
    }

    async def fake_download(url: str, scratch: Path) -> Path:
        state["input_url"] = url
        p = scratch / "in.mp3"
        p.write_bytes(b"input")
        return p

    async def fake_upload(path: Path, url: str) -> None:
        state["uploaded"][url] = path.read_bytes()
        if "vocals_output_url" == url or url == state["vocals_url"]:
            pass

    monkeypatch.setattr(sep_router, "download_to_temp", fake_download)
    monkeypatch.setattr(sep_router, "upload_from_path", fake_upload)
    return state


def test_separate_happy_path(stub_io):
    stub = _StubDemucs()
    app.dependency_overrides[get_demucs_service] = lambda: stub
    stub_io["vocals_url"] = "https://r2.test/v.wav"
    stub_io["no_vocals_url"] = "https://r2.test/nv.wav"
    try:
        client = TestClient(app)
        resp = client.post("/separate", json={
            "input_url": "https://r2.test/in.mp3",
            "vocals_output_url": stub_io["vocals_url"],
            "no_vocals_output_url": stub_io["no_vocals_url"],
        })
        assert resp.status_code == 204
        assert stub_io["input_url"] == "https://r2.test/in.mp3"
        assert stub_io["uploaded"][stub_io["vocals_url"]] == b"VOCALS"
        assert stub_io["uploaded"][stub_io["no_vocals_url"]] == b"NO-VOCALS"
        assert len(stub.calls) == 1
    finally:
        app.dependency_overrides.clear()


@pytest.mark.parametrize(
    "body",
    [
        {"vocals_output_url": "v", "no_vocals_output_url": "nv"},
        {"input_url": "i", "no_vocals_output_url": "nv"},
        {"input_url": "i", "vocals_output_url": "v"},
        {"input_url": "", "vocals_output_url": "v", "no_vocals_output_url": "nv"},
    ],
    ids=["missing-input", "missing-vocals", "missing-novocals", "empty-input"],
)
def test_separate_missing_required_fields_returns_422(body, stub_io):
    client = TestClient(app)
    resp = client.post("/separate", json=body)
    assert resp.status_code == 422


def test_separate_runtime_error_returns_500(stub_io):
    stub = _StubDemucs(raise_exc=RuntimeError("demucs OOM"))
    app.dependency_overrides[get_demucs_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post("/separate", json={
            "input_url": "i",
            "vocals_output_url": "v",
            "no_vocals_output_url": "nv",
        })
        assert resp.status_code == 500
        assert "demucs" in resp.json()["detail"]
    finally:
        app.dependency_overrides.clear()
```

- [ ] **Step 2: Rewrite `routers/separate.py`**

```python
from __future__ import annotations

import asyncio
import os
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Response
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp, upload_from_path
from services.demucs_service import DemucsService


class SeparateRequest(BaseModel):
    input_url: str = Field(min_length=1)
    vocals_output_url: str = Field(min_length=1)
    no_vocals_output_url: str = Field(min_length=1)


@lru_cache(maxsize=1)
def get_demucs_service() -> DemucsService:
    return DemucsService(device=os.environ.get("DEVICE", "cpu"))


SeparateServiceDep = Annotated[DemucsService, Depends(get_demucs_service)]
router = APIRouter()


@router.post("/separate", status_code=204)
def separate(req: SeparateRequest, service: SeparateServiceDep) -> Response:
    """Download input → run Demucs → upload both stems."""

    async def _run() -> None:
        with tempfile.TemporaryDirectory(prefix="separate-") as td:
            scratch = Path(td)
            src = await download_to_temp(req.input_url, scratch)
            stems_dir = scratch / "stems"
            stems_dir.mkdir()
            try:
                service.separate(str(src), str(stems_dir))
            except RuntimeError as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc
            vocals = stems_dir / "vocals.wav"
            no_vocals = stems_dir / "no_vocals.wav"
            if not vocals.exists() or not no_vocals.exists():
                raise HTTPException(status_code=500, detail="demucs did not produce both stems")
            await upload_from_path(vocals, req.vocals_output_url)
            await upload_from_path(no_vocals, req.no_vocals_output_url)

    asyncio.run(_run())
    return Response(status_code=204)
```

- [ ] **Step 3: Run Python tests**

```bash
cd audio-processor && python -m pytest tests/test_separate_router.py -v
```

Expected: PASS.

### Go side

- [ ] **Step 4: Update `job_runner.go` Stage 2 (Separate)**

In `Run`, replace the Stage 2 block with:

```go
    // Stage 2: Separate vocals from instrumental.
    r.update(jobID, models.StatusSeparating, "separating vocals from instrumental")
    vocalsKey := r.storage.Key(videoID, "vocals.wav")
    noVocalsKey := r.storage.Key(videoID, "no_vocals.wav")
    vocalsHas, _ := r.storage.Has(ctx, vocalsKey)
    noVocalsHas, _ := r.storage.Has(ctx, noVocalsKey)
    if !vocalsHas || !noVocalsHas {
        inURL, err := r.storage.SignGet(ctx, r.storage.Key(videoID, "original.wav"))
        if err != nil {
            r.fail(jobID, "sign get failed: "+err.Error())
            return
        }
        vocalsPutURL, err := r.storage.SignPut(ctx, vocalsKey)
        if err != nil {
            r.fail(jobID, "sign put failed: "+err.Error())
            return
        }
        noVocalsPutURL, err := r.storage.SignPut(ctx, noVocalsKey)
        if err != nil {
            r.fail(jobID, "sign put failed: "+err.Error())
            return
        }
        if err := r.gpu.Separate(ctx, inURL, vocalsPutURL, noVocalsPutURL); err != nil {
            r.fail(jobID, "separate failed: "+err.Error())
            return
        }
        if err := r.storage.Verify(ctx, vocalsKey); err != nil {
            r.fail(jobID, "vocals stem not materialized: "+err.Error())
            return
        }
        if err := r.storage.Verify(ctx, noVocalsKey); err != nil {
            r.fail(jobID, "no_vocals stem not materialized: "+err.Error())
            return
        }
    }
```

- [ ] **Step 5: Update `preview_stems.go` Stage 2**

Same shape: replace the `local, ok := storage.(*services.LocalDiskStorage)` + path construction + `processor.Separate(ctx, previewPath, outputDir)` block with the SignGet/SignPut/`gpu.Separate`/Verify pattern using the keys `preview-stems/vocals.wav` and `preview-stems/no_vocals.wav`. Drop the `os.MkdirAll(outputDir)` call.

Change handler signature — add `gpu` alongside the existing `processor` arg. Stage 4 Melody continues to use `processor` for one more task; Task 8 swaps it to `gpu` and Task 9 drops `processor` entirely:

```go
func PreviewStems(
    signer *services.Signer,
    storage services.Storage,
    ytSvc services.YouTubeService,
    processor services.ProcessorClient, // legacy; used by Stage 4 until Task 8
    gpu services.GPUProcessorClient,
    transcode services.TranscodeFunc,
) http.HandlerFunc {
```

- [ ] **Step 6: Update tests**

- `backend/services/job_runner_test.go`: extend `fakeGPUJob` with a `separateFn` field that the test stages stems for, mirroring Task 5's `fakeCPUJob` pattern. Tests need to write `vocals.wav` and `no_vocals.wav` into storage so `Verify` passes.
- `backend/api/handlers/preview_stems_test.go`: same pattern for the preview path; the fake's `Separate` writes `preview-stems/vocals.wav` and `preview-stems/no_vocals.wav` into the test's `LocalDiskStorage`.

- [ ] **Step 7: Wire `gpu` in router and main**

In `router.go`, pass `gpu` to `handlers.PreviewStems`. In `main.go`, no changes (already passing `gpuProc`).

- [ ] **Step 8: Build + test**

```bash
cd backend && go build ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add audio-processor/routers/separate.py audio-processor/tests/test_separate_router.py \
        backend/services/job_runner.go backend/services/job_runner_test.go \
        backend/api/handlers/preview_stems.go backend/api/handlers/preview_stems_test.go \
        backend/api/router.go
git commit -m "feat(processor): migrate /separate to URL handoff"
```

---

## Task 8: Migrate `/melody` end-to-end

**Goal:** Final endpoint migration. Same template as `/shift`.

**Files:**
- Modify: `audio-processor/routers/melody.py`
- Modify: `audio-processor/tests/test_melody_router.py`
- Modify: `backend/services/job_runner.go`
- Modify: `backend/services/job_runner_test.go`
- Modify: `backend/api/handlers/preview_stems.go`
- Modify: `backend/api/handlers/preview_stems_test.go`
- Modify: `backend/api/router.go`

### Python side

- [ ] **Step 1: Rewrite Python tests for `/melody`**

Apply the same Task 5 / Task 7 template to `test_melody_router.py`:
- `_StubMelodyService.extract(vocals_path, output_path)` writes `Path(output_path).write_bytes(b'{"key":"A","series":[]}')`.
- Stub `download_to_temp` + `upload_from_path` on `routers.melody`.
- Happy path: POST `{"vocals_input_url": "...", "output_url": "..."}` → 200; assert upload received the JSON bytes.
- 422 tests for missing/empty `vocals_input_url`/`output_url`.
- 500 test for RuntimeError propagation.

- [ ] **Step 2: Rewrite `routers/melody.py`**

```python
from __future__ import annotations

import asyncio
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp, upload_from_path
from services.melody_service import MelodyService


class MelodyRequest(BaseModel):
    vocals_input_url: str = Field(min_length=1)
    output_url: str = Field(min_length=1)


@lru_cache(maxsize=1)
def get_melody_service() -> MelodyService:
    return MelodyService()


MelodyServiceDep = Annotated[MelodyService, Depends(get_melody_service)]
router = APIRouter()


@router.post("/melody")
def melody(req: MelodyRequest, service: MelodyServiceDep) -> dict:
    """Download vocals stem → extract melody → upload melody.json."""

    async def _run() -> None:
        with tempfile.TemporaryDirectory(prefix="melody-") as td:
            scratch = Path(td)
            src = await download_to_temp(req.vocals_input_url, scratch)
            dst = scratch / "melody.json"
            try:
                service.extract(str(src), str(dst))
            except RuntimeError as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc
            await upload_from_path(dst, req.output_url)

    asyncio.run(_run())
    return {}
```

- [ ] **Step 3: Python tests pass**

```bash
cd audio-processor && python -m pytest tests/test_melody_router.py -v
```

### Go side

- [ ] **Step 4: Update `job_runner.go` Stage 3 (Melody)**

```go
    // Stage 3: Extract melody from vocals stem.
    r.update(jobID, models.StatusMelody, "extracting melody")
    melodyKey := r.storage.Key(videoID, "melody.json")
    melodyHas, _ := r.storage.Has(ctx, melodyKey)
    if !melodyHas {
        vocalsURL, err := r.storage.SignGet(ctx, r.storage.Key(videoID, "vocals.wav"))
        if err != nil {
            r.fail(jobID, "sign get failed: "+err.Error())
            return
        }
        outURL, err := r.storage.SignPut(ctx, melodyKey)
        if err != nil {
            r.fail(jobID, "sign put failed: "+err.Error())
            return
        }
        if err := r.gpu.Melody(ctx, vocalsURL, outURL); err != nil {
            r.fail(jobID, "melody failed: "+err.Error())
            return
        }
        if err := r.storage.Verify(ctx, melodyKey); err != nil {
            r.fail(jobID, "melody not materialized: "+err.Error())
            return
        }
    }
```

- [ ] **Step 5: Update `preview_stems.go` Stage 4 (Melody)**

```go
    if !melodyHas {
        vocalsKey := storage.Key(videoID, "preview-stems/vocals.wav")
        melodyKey := storage.Key(videoID, "preview-stems/melody.json")
        vocalsURL, err := storage.SignGet(ctx, vocalsKey)
        if err != nil {
            log.Error().Err(err).Msg("storage.SignGet (vocals.wav) failed")
            writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign get failed"})
            return
        }
        outURL, err := storage.SignPut(ctx, melodyKey)
        if err != nil {
            log.Error().Err(err).Msg("storage.SignPut (melody.json) failed")
            writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign put failed"})
            return
        }
        if err := gpu.Melody(ctx, vocalsURL, outURL); err != nil {
            log.Error().Err(err).Str("videoId", videoID).Msg("processor.Melody failed")
            writeJSON(w, http.StatusBadGateway, errorResponse{Error: "melody failed"})
            return
        }
        if err := storage.Verify(ctx, melodyKey); err != nil {
            log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (melody.json) failed")
            writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "melody not materialized"})
            return
        }
    }
```

- [ ] **Step 6: Update tests**

- `job_runner_test.go`: extend `fakeGPUJob` with `melodyFn`; in tests, write `melody.json` to storage from the fake so `Verify` passes.
- `preview_stems_test.go`: same.

- [ ] **Step 7: Build + test**

```bash
cd backend && go build ./... && go test ./...
```

- [ ] **Step 8: End-to-end local smoke**

```bash
cd backend && go run ./cmd/server &
cd audio-processor && uvicorn main:app --reload --port 8090 &
# Frontend or curl:
curl -s "localhost:8080/api/songs/search?q=hello+adele" | jq '.[0]'
# Drive preview → preview-shift → generate → audio/melody for one videoId.
```

Expected: same behavior as before the refactor; full pipeline produces audio and melody.

- [ ] **Step 9: Commit**

```bash
git add audio-processor/routers/melody.py audio-processor/tests/test_melody_router.py \
        backend/services/job_runner.go backend/services/job_runner_test.go \
        backend/api/handlers/preview_stems.go backend/api/handlers/preview_stems_test.go \
        backend/api/router.go
git commit -m "feat(processor): migrate /melody to URL handoff"
```

---

## Task 9: Remove legacy `ProcessorClient`, `FilesystemPathForLocalProcessor`, and cleanup

**Goal:** Delete every artifact left over from the path-based protocol. `STORAGE_BACKEND=r2` is now fully operational because no code path requires a `*LocalDiskStorage` type assertion.

**Files:**
- Modify: `backend/services/processor.go` — delete combined `ProcessorClient` interface and `PythonProcessorClient` type.
- Modify: `backend/services/processor_test.go` — delete tests for old `PythonProcessorClient`.
- Modify: `backend/services/storage.go` — delete `FilesystemPathForLocalProcessor`.
- Modify: `backend/services/job_runner.go` — drop `processor ProcessorClient` field/arg.
- Modify: `backend/services/job_runner_test.go` — drop the now-unused fake processor arg.
- Modify: `backend/api/router.go` — drop `processor` arg.
- Modify: `backend/cmd/server/main.go` — drop `processor` var.
- Modify: `backend/api/handlers/preview_key.go` — drop unused `processor` arg.
- Modify: `backend/api/handlers/preview_key_test.go` — same.

- [ ] **Step 1: Sanity-check no live callers remain**

```bash
cd backend && grep -rn "FilesystemPathForLocalProcessor\|services\.ProcessorClient\b\|NewPythonProcessorClient\|PythonProcessorClient\b" --include="*.go" .
```

Expected: only the definitions inside `processor.go` and `storage.go`, plus a few `_test.go` fakes. If any production code path still references them, that's a Task 5–8 leftover — fix there.

- [ ] **Step 2: Delete `FilesystemPathForLocalProcessor`**

In `backend/services/storage.go`, remove the method (and its comment block) at the bottom.

- [ ] **Step 3: Delete combined `ProcessorClient` and `PythonProcessorClient`**

In `backend/services/processor.go`, delete:
- The `ProcessorClient` interface.
- The `PythonProcessorClient` struct.
- `NewPythonProcessorClient`.
- All four methods (`Shift`, `Separate`, `Melody`, `PreviewKey`) on `PythonProcessorClient`.
- The `separateResponse` and `previewKeyResponse` types if unused elsewhere (grep first).

The file becomes empty if nothing else lives in it — in that case `git rm backend/services/processor.go`.

- [ ] **Step 4: Delete old tests**

In `backend/services/processor_test.go`, delete every `TestPythonProcessorClient_*` test. Test names should now all be `TestPythonCPUProcessorClient_*` / `TestPythonGPUProcessorClient_*` from Task 4. If the file ends up empty, `git rm backend/services/processor_test.go`.

- [ ] **Step 5: Remove `processor` from `JobRunner`**

In `backend/services/job_runner.go`, drop the `processor ProcessorClient` field and its constructor arg. Update `NewJobRunner` signature:

```go
func NewJobRunner(
    ytSvc YouTubeService,
    storage Storage,
    cpu CPUProcessorClient,
    gpu GPUProcessorClient,
    jobStore *JobStore,
    maxConcurrent int,
) *JobRunner {
```

- [ ] **Step 6: Remove `processor` from router**

In `backend/api/router.go`, drop `processor services.ProcessorClient` from `NewRouter`. Verify the body of `NewRouter` no longer references it.

- [ ] **Step 7: Remove `processor` from `preview_key.go` handler**

Replace the handler signature in `backend/api/handlers/preview_key.go`:

```go
func PreviewKey(
    signer *services.Signer,
    storage services.Storage,
    _ services.YouTubeService,
) http.HandlerFunc {
```

(Drop the `_ services.ProcessorClient` arg.) Update the router wiring accordingly.

- [ ] **Step 8: Update `main.go`**

In `backend/cmd/server/main.go`, delete the `processor := services.NewPythonProcessorClient(...)` line, and drop `processor` from the `NewJobRunner` and `api.NewRouter` calls.

- [ ] **Step 9: Update test fakes**

Search `_test.go` files for `services.ProcessorClient` and remove the old fakes / drop the now-unused arg. Grep:

```bash
cd backend && grep -rn "services\.ProcessorClient\b" --include="*.go" .
```

Expected after fix: empty.

- [ ] **Step 10: Build + test**

```bash
cd backend && go build ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 11: Boot both backends to ensure no missing wiring**

```bash
cd backend && STORAGE_BACKEND=local go run ./cmd/server
# Ctrl+C, then with fake R2 creds:
cd backend && STORAGE_BACKEND=r2 R2_ACCOUNT_ID=fake R2_ACCESS_KEY_ID=k \
    R2_SECRET_ACCESS_KEY=s R2_BUCKET=b go run ./cmd/server
```

Expected: both boot without error. R2-mode generate would fail on the first real R2 call (no actual bucket), which is fine — we're confirming the type-assertion escape hatches are gone.

- [ ] **Step 12: Commit**

```bash
git add backend/
git commit -m "refactor(processor): remove legacy ProcessorClient and local-path escape hatch"
```

---

## Task 10: Update `.env.example`, CLAUDE.md, and end-to-end smoke

**Goal:** Docs reflect the new architecture; one full local-mode flow verified.

**Files:**
- Modify: `backend/.env.example`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update `.env.example`**

Replace the deprecated `PYTHON_PROCESSOR_URL` doc with a note that `CPU_PROCESSOR_URL` and `GPU_PROCESSOR_URL` are the canonical names; `PYTHON_PROCESSOR_URL` is the fallback for both. Make sure the four env vars from Task 2 are documented (uncommented if they have defaults, commented if they're examples).

- [ ] **Step 2: Update `CLAUDE.md`**

In the **Architecture** section, replace the `Storage interface` bullet with:

> `Storage` interface in `backend/services/storage.go`: handlers operate on opaque keys via `Key/Has/SignGet/SignPut/Commit/Open/Verify`. Python services receive presigned URLs (never filesystem paths); they download → process → upload. `LocalDiskStorage` mints `/internal/blob/{key}` URLs in local dev; `R2Storage` mints real R2 presigned URLs. Selected by `STORAGE_BACKEND` (`local` or `r2`).

Add a new bullet under **Important Notes**:

> `ProcessorClient` is split into `CPUProcessorClient` (Shift, PreviewKey) and `GPUProcessorClient` (Separate, Melody). Both are URL-based. During Plan #2 both point at the single Python service via `CPU_PROCESSOR_URL` / `GPU_PROCESSOR_URL` (each defaulting to `PYTHON_PROCESSOR_URL`); the Python service split plan splits them onto two services.

- [ ] **Step 3: End-to-end smoke (local mode)**

```bash
# Terminal 1:
cd backend && go run ./cmd/server

# Terminal 2:
cd audio-processor && uvicorn main:app --reload --port 8090

# Terminal 3 — full flow:
curl -s "localhost:8080/api/songs/search?q=adele+hello" | tee /tmp/search.json
VID=$(jq -r '.[0].videoId' /tmp/search.json)
SIG=$(jq -r '.[0].sig' /tmp/search.json)
echo "vid=$VID sig=$SIG"

# Preview (~5s cold):
curl -s -o /tmp/preview.mp3 "localhost:8080/api/preview/$VID?sig=$SIG"
file /tmp/preview.mp3

# Preview-shift (~1-2s):
curl -s -o /tmp/preview-shift.mp3 -X POST localhost:8080/api/preview-shift \
  -H 'content-type: application/json' \
  -d "{\"video_id\":\"$VID\",\"sig\":\"$SIG\",\"semitones\":-3}"
file /tmp/preview-shift.mp3

# Generate (~90-180s):
JOB=$(curl -s -X POST localhost:8080/api/generate \
  -H 'content-type: application/json' \
  -d "{\"video_id\":\"$VID\",\"sig\":\"$SIG\",\"semitones\":-3}" | jq -r .job_id)
# Follow SSE until done:
curl -N localhost:8080/api/status/$JOB

# Final artifacts:
curl -s -o /tmp/full.mp3 "localhost:8080/api/audio/$VID/-3?sig=$SIG" && file /tmp/full.mp3
curl -s "localhost:8080/api/melody/$VID/-3?sig=$SIG" | jq '.key'
```

Expected: all three audio files are real MP3s; melody returns a JSON with a non-empty `key`.

- [ ] **Step 4: Commit**

```bash
git add backend/.env.example CLAUDE.md
git commit -m "docs: document URL-handoff processor split"
```

---

## Self-review checklist (engineer running this plan)

Before opening PR / merging:

1. `cd backend && go build ./... && go test ./...` passes.
2. `cd audio-processor && python -m pytest -q` passes (the four router test files + `test_io_url.py`).
3. `STORAGE_BACKEND=local` end-to-end: search → preview → preview-shift → generate → audio → melody all work (per Task 10 Step 3).
4. `STORAGE_BACKEND=r2` boots without error against fake creds.
5. No remaining references to `FilesystemPathForLocalProcessor`, `services.ProcessorClient` (the old combined interface), `PythonProcessorClient`, or `input_path` / `output_path` in any router/handler.
6. Grep `backend/api/handlers/` for `os.Open`, `os.MkdirTemp`, `os.RemoveAll`, `filepath.Join` — should be drastically reduced or gone in the migrated handlers (`preview_shift.go`, `preview_stems.go`). Anything that remains should be unrelated to the processor flow.
7. `.env.example` lists `CPU_PROCESSOR_URL`, `GPU_PROCESSOR_URL`, `CPU_PROCESSOR_TIMEOUT_SECONDS`, `GPU_PROCESSOR_TIMEOUT_SECONDS`.
