# Cantus Deployment Design

**Date:** 2026-06-13
**Status:** Design locked. Implementation pending, to be split into per-subsystem plans.
**Related:** `CLAUDE.md` (current architecture), `FLOW.md` (end-to-end pipeline), memory `project-deployment-plan`, memory `project-ytdlp-antibot`.

---

## 1. Goal

Take Cantus from "runs on one laptop with `tmp/cache/` and a single Python monolith" to "runs as a small public beta on managed infra, with anti-abuse, and with GPU cost minimized." No login. Beta-grade reliability, not production SLA.

## 2. Non-goals

- User accounts / auth.
- Horizontal scale beyond one Fly machine of each kind (we'll add when load demands).
- Cache eviction (R2 free tier covers us until ~10GB; defer LRU until then).
- Production-grade anti-abuse (residential proxies, captcha-on-everything). Beta-grade only.
- Migration of existing `tmp/cache/` content to R2. Caches regenerate on demand; no backfill.

## 3. Target topology

```
                            ┌────────────────────────┐
                            │  Cloudflare (edge)     │
                            │  - WAF rate limits     │
                            │  - Turnstile           │
                            └────────────┬───────────┘
                                         │
                ┌────────────────────────┴───────────────────────┐
                │                                                │
   ┌────────────▼────────────┐                  ┌────────────────▼─────────────┐
   │  Cloudflare Pages       │                  │  Fly.io                       │
   │  Vue 3 frontend         │                  │  Go backend (shared-cpu-1x)   │
   └─────────────────────────┘                  │  - HTTP API                   │
                                                │  - SSE                        │
                                                │  - yt-dlp (PoT-enabled)       │
                                                └──┬────────────┬──────────────┘
                                                   │            │
                              ┌────────────────────┘            └────────────────────┐
                              │                                                       │
                  ┌───────────▼─────────────┐                          ┌──────────────▼────────────┐
                  │  Fly.io                  │                          │  Hugging Face Spaces       │
                  │  Python CPU service      │                          │  Python GPU service         │
                  │  - /search (ytmusicapi)  │                          │  (ZeroGPU)                  │
                  │  - /shift  (rubberband)  │                          │  - /separate (Demucs)       │
                  │  - /preview-key (librosa)│                          │  - /melody   (CREPE)        │
                  └──────────────────────────┘                          └─────────────────────────────┘
                              │                                                       │
                              └────────────┬──────────────────────────────────────────┘
                                           │
                                ┌──────────▼──────────┐
                                │  Cloudflare R2      │
                                │  cantus-cache       │
                                │  (presigned URLs)   │
                                └─────────────────────┘
```

Plus one more Fly app: **bgutil PoT provider sidecar**, called only by the Go container's yt-dlp.

## 4. Locked design decisions

These were settled in brainstorming on 2026-06-13. Don't re-litigate without new constraints.

### 4.1 Storage handoff (Go ↔ Python)

**Decision:** Go mints both input GET and output PUT presigned URLs upfront and sends them to Python in one request. Python downloads, processes, uploads, returns 200.

**Why:** Stateless Python (no R2 creds, no key knowledge, no callbacks). One round-trip. Matches our small file sizes (≤30MB stems) and bounded job durations (≤120s ZeroGPU budget). HF→Fly callback would couple two flaky directions; this couples one.

**Implication:** 10-min TTL on presigned URLs covers worst-case job duration with margin. If Demucs ever exceeds that, we revisit (see §10 risks).

### 4.2 R2 key scheme ownership

**Decision:** Go owns the R2 key scheme. Python only sees opaque presigned URLs.

**Why:** Key scheme changes only require a Go-side deploy. Python services are swappable / replaceable without coordinating key contracts.

**Implication:** `SignGet`/`SignPut` are methods on the `Storage` interface, not standalone utilities.

### 4.3 Local dev parity

**Decision:** Keep `LocalDiskStorage` as a `Storage` impl. Select via `STORAGE_BACKEND` env var (`local` | `r2`). `LocalDiskStorage.SignGet/SignPut` return HMAC-signed URLs to a Go-side `/internal/blob/...` handler that serves/accepts files from `tmp/cache/`.

**Why:** `go run ./...` keeps working offline with no R2 creds. CI tests both impls through the same interface. Python services don't know the difference — they just hit the URL they were given.

**Implication:** A `/internal/blob/{key}` handler exists in Go, gated by HMAC token (reuse `VIDEO_ID_SIGNING_KEY`). This handler must NOT be exposed to the public internet — it's bound to localhost in local dev and the route doesn't exist in the `r2` backend mode.

### 4.4 Cache existence checks

**Decision:** `R2Storage.Has()` does a HEAD request per call. No in-memory caching of results.

**Why:** Adding ~200ms to a 90-180s generate or a 5s preview is rounding error. Cache invalidation isn't worth solving for a problem we don't have. R2 strong consistency means HEAD is always correct.

**Implication:** `Has()` returns true iff HEAD returns 200 AND `Content-Length > 0` (preserves existing zero-byte = corrupt semantic).

### 4.5 Python service split

**Decision:** Split the current monolithic Python service:
- **GPU service (HF Space, ZeroGPU):** `/separate` (Demucs), `/melody` (CREPE). Decorated with `@spaces.GPU(duration=120)`. Models loaded at import time so GPU budget isn't burned on cold load.
- **CPU service (Fly, shared-cpu-1x):** `/search` (ytmusicapi), `/shift` (rubberband), `/preview-key` (librosa). `auto_stop_machines = "stop"` so idle ≈ $0.

**Why:** ZeroGPU quota and cold-start cost are precious. CPU work (search, shift, key estimation) doesn't need GPU and shouldn't burn it. Smaller CPU image (~400MB without torch/demucs/crepe) cold-starts faster on Fly.

**Implication:** `ProcessorClient` interface splits into `CPUProcessorClient` and `GPUProcessorClient` (different base URLs, different timeouts).

### 4.6 yt-dlp anti-bot: bgutil PoT only (no Hetzner in v1)

**Decision:** First deployment ships with the bgutil PoT provider plugin and nothing else — no Hetzner, no cookies, no proxy.

**Why:** Goal is to measure whether PoT alone holds against real beta load before adding cost or complexity. If PoT proves insufficient (sustained 429s/bot challenges), Hetzner VPS ($4/mo) is the documented fallback (see memory `project-ytdlp-antibot`).

**Implication:** Go's Dockerfile needs Python so `bgutil-ytdlp-pot-provider` can install into yt-dlp's venv. A separate small Fly app runs the bgutil HTTP provider. Go passes `--extractor-args "youtubepot-bgutilhttp:base_url=$YT_DLP_POT_BASE_URL"` to yt-dlp when the env var is set (so local dev without sidecar still works).

### 4.7 Anti-abuse stack

**Decision:** Defense in depth:
1. **HMAC sig** (existing) — payload becomes `videoID|sessionID|issuedAt` with short TTL (~30 min). Stops sig replay across IPs/sessions.
2. **Cloudflare Turnstile** on `/api/songs/search` and `/api/generate` only (not `/api/preview-shift` — user is mid-interaction, captcha would be hostile).
3. **Cloudflare WAF rate limits** — e.g. 30 searches/min/IP, 5 generates/hr/IP.
4. **Frontend session_id** — UUID in localStorage, sent on every audio call.

**Why:** No login means no per-user accountability. Three independent layers (sig binding, captcha, WAF) means defeating one isn't enough.

**Implication:** Sig payload format changes — existing `{videoID}` sigs become invalid. Acceptable: users re-search. Document the rotation event.

## 5. Storage interface — target shape

```go
// backend/services/storage.go

type Storage interface {
    // Key returns the storage key for (videoID, name). Pure function, no I/O.
    // Replaces the old LocalPath (which exposed a filesystem path).
    Key(videoID, name string) string

    // Has reports whether the object exists with non-zero size.
    Has(ctx context.Context, key string) (bool, error)

    // SignGet returns a presigned GET URL valid for the configured TTL.
    SignGet(ctx context.Context, key string) (string, error)

    // SignPut returns a presigned PUT URL valid for the configured TTL.
    SignPut(ctx context.Context, key string) (string, error)

    // Commit is called by Go after a successful Python upload to verify the
    // object exists and meets size requirements. Idempotent.
    Commit(ctx context.Context, key string) error

    // Open returns a ReadCloser for serving the object to the browser.
    // For R2Storage this streams from R2; for LocalDiskStorage this reads tmp/cache/.
    Open(ctx context.Context, key string) (io.ReadCloser, error)
}
```

Both `LocalDiskStorage` and `R2Storage` implement this. `LocalPath` is removed — no caller may construct a filesystem path. Handlers that today read a local path for `http.ServeFile` switch to `storage.Open()` + `io.Copy`.

## 6. Processor client — target shape

```go
// backend/services/processor.go

type CPUProcessorClient interface {
    Search(ctx context.Context, q string) ([]SearchResult, error)
    Shift(ctx context.Context, inputURL, outputURL string, semitones float64) error
    PreviewKey(ctx context.Context, inputURL string) (string, error)
}

type GPUProcessorClient interface {
    Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error
    Melody(ctx context.Context, vocalsInputURL, outputURL string) error
}
```

Notes:
- All inputs/outputs are URLs (presigned by Go from `Storage.SignGet/SignPut`).
- `Search` moves into CPU client (currently lives in the monolithic Python service).
- `Separate` takes both output URLs upfront (Demucs produces 2 stems; Go signs both).
- HTTP timeouts: CPU ~10s, GPU ~180s (with separate dial vs read timeouts; HF cold start can be 30s).

## 7. Frontend changes

- `VITE_API_BASE_URL` → Fly Go host.
- Cloudflare Pages build config + env vars.
- Turnstile widget on search page; token attached to `/api/songs/search` and `/api/generate` requests.
- `session_id` UUID generated on first load, stored in localStorage, sent on every audio call.
- SSE reconnect logic — Fly proxies may drop long idle streams during Demucs (60-120s of no progress events). Reconnect with last `Last-Event-ID`.

## 8. Configuration matrix

New env vars on Go backend:

| Var | Purpose | Required when |
|---|---|---|
| `STORAGE_BACKEND` | `local` or `r2` | always |
| `R2_ACCOUNT_ID` | R2 account | `r2` only |
| `R2_ACCESS_KEY_ID` | R2 token | `r2` only |
| `R2_SECRET_ACCESS_KEY` | R2 token | `r2` only |
| `R2_BUCKET` | R2 bucket name | `r2` only |
| `R2_PRESIGN_TTL_SECONDS` | URL TTL (default 600) | `r2` only |
| `CPU_PROCESSOR_URL` | Fly Python-CPU base URL | always |
| `GPU_PROCESSOR_URL` | HF Space base URL | always |
| `YT_DLP_POT_BASE_URL` | bgutil provider URL | when PoT enabled (always in prod) |
| `TURNSTILE_SECRET` | Cloudflare Turnstile server-side secret | prod only |
| `SESSION_SIG_TTL_SECONDS` | HMAC sig TTL (default 1800) | always |
| `ALLOWED_ORIGINS` | CORS allowlist | prod only |
| `VIDEO_ID_SIGNING_KEY` | (existing) HMAC key | always |

## 9. Subsystem implementation order

Each subsystem will have its own plan file. Order chosen for testability — each step works end-to-end before moving on.

1. **Go backend refactor** — `Storage` interface, presigned URLs, processor client split. `LocalDiskStorage` keeps everything green. Verified with existing manual flows.
2. **Python CPU service split** — extract `/search`, `/shift`, `/preview-key` into new FastAPI app. Test against local Go with `CPU_PROCESSOR_URL=http://localhost:8091`.
3. **Python GPU service** — strip down to `/separate` + `/melody`. Test locally first, then deploy to HF Space.
4. **Infra setup** — R2 bucket, Fly apps (Go, Python-CPU, bgutil sidecar), HF Space, Pages project, Turnstile, WAF rules. Mostly parallel.
5. **Frontend deployment changes** — env var, Turnstile widget, session_id, SSE reconnect.
6. **End-to-end on managed infra** — flip env vars, smoke test, ship.

Plans for #2-#6 will be written incrementally — we may learn things during #1 that change them.

## 10. Risks and mitigations

| Risk | Mitigation |
|---|---|
| PoT-on-Fly insufficient against YouTube bot detection | Fallback to Hetzner VPS ($4/mo). `YouTubeService` interface already supports the swap. |
| Demucs on a 4-min song exceeds 120s ZeroGPU budget | Fall back to `mdx_extra_q` (lighter model). If still over: raise PUT URL TTL or chunk separation. |
| Presigned PUT URL expires mid-upload on slow HF network | Bump `R2_PRESIGN_TTL_SECONDS` to 900. If still flaky, switch to Option B handoff (Python callback for fresh URL). |
| Storage interface refactor touches every handler, big blast radius | TDD per task. `LocalDiskStorage` keeps local dev green throughout. Don't split into multiple PRs — atomic switch. |
| Cloudflare Turnstile blocks legitimate users (false positives) | Use "Managed" challenge difficulty initially. Monitor 4xx rate; downgrade to "Non-interactive" if too noisy. |
| R2 free tier exceeded sooner than expected | Add LRU eviction. Existing `Storage` interface already supports a `Delete` method addition. |
| Fly SSE proxy buffers responses, frontend never sees progress | Set short flush interval / disable buffering in Go SSE handler. Verify with `curl -N` against deployed app. |

## 11. Out of scope (explicitly deferred)

- LRU cache eviction in R2.
- Multi-region deployment.
- Hetzner VPS yt-dlp fallback (only if §10 risk materializes).
- Production-grade monitoring/alerting (basic Fly logs + HF Space logs are enough for beta).
- User-visible error pages for WAF blocks (Cloudflare default page is acceptable for beta).
- Cost dashboards / budget alerts (manually check Fly/HF/R2 usage weekly during beta).
