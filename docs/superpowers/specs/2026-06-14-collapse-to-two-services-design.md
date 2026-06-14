# Collapse Cantus to Two Services: Go (AWS) + Python GPU (HF Space)

**Date:** 2026-06-14
**Status:** Design locked. Implementation plan pending.
**Supersedes:** the earlier "Python service split" spec (same file path, replaced 2026-06-14).
**Related:** `docs/superpowers/specs/2026-06-13-deployment-design.md` §4.5, `docs/superpowers/specs/2026-06-14-processor-url-handoff-design.md`, memory `project-deployment-plan`.

---

## 1. Goal

Reduce the Python service surface area to exactly the work that actually needs a GPU. Move `/search` and `/shift` into the Go backend; delete the dead `/preview-key` Python path; trim `audio-processor/` down to `/separate` + `/melody` only and rename it to `audio-processor-gpu/`. After this plan, cantus runs as **two services**:

- **Go backend** on AWS EC2 t3.micro (free tier). Owns API + SSE + yt-dlp + bgutil PoT sidecar + ytmusic-search + audio-shift.
- **Python GPU service** on Hugging Face ZeroGPU Space. Owns only Demucs (`/separate`) and CREPE (`/melody`).

This unblocks the soft-launch deployment with the smallest possible operational footprint.

## 2. Why this shape (not the earlier "split into 2 Python services")

The earlier plan kept Python on AWS because Fly.io economics made HF cold-start the only painful trade-off. Two facts changed the math:

1. **Fly.io ended their free tier;** the new AWS EC2 t3.micro target has 1 GB RAM — tight for Go + Python + bgutil.
2. **`github.com/raitonoberu/ytmusic` works.** Verified on 2026-06-14 against five live queries (Bohemian Rhapsody, Adele Hello, Taylor Swift Cardigan, Weezer Say It Ain't So, Radiohead Karma Police): identical canonical videoIds to ytmusicapi, all required fields present (videoId, title, artists, album, duration in seconds, thumbnails), song-entity filtering via `TrackFilter` enum, pagination via `s.Next()`. Library is stale (last commit March 2024, 24 stars) but YouTube Music's web API has been stable enough that it still works today.

With raitonoberu/ytmusic viable, the only Python CPU work that genuinely needs to stay in Python is — nothing. `/shift` is just `pyrubberband` shelling out to the `rubberband` CLI, which Go can shell to directly. `/preview-key` is dead code (the handler reads `melody.json`, never calls the Python endpoint). `/search` is the only Python-locked piece, and the Go library covers it.

## 3. Non-goals

- HF Space `app.py` + `@spaces.GPU(duration=120)` decorator + pre-baked Demucs weights. Deferred to the deployment-packaging plan.
- Dockerfiles, EC2 user-data scripts, R2 bucket provisioning. Deferred to the packaging plan.
- yt-dlp PoT provider packaging on EC2.
- Anti-abuse stack (sig payload change, Turnstile, WAF, CORS lock-down). Deferred to a later plan; soft-launch ships without it.
- Frontend deploy prep (Pages env vars, session_id, SSE reconnect).
- Removing the legacy `PYTHON_PROCESSOR_URL` env var. Stays as the default-fallback for `PROCESSOR_URL` so existing dev environments don't break.
- Vendoring or forking `raitonoberu/ytmusic`. If it breaks in the future, we vendor then; not now.

## 4. Target topology

```
                ┌─ Cloudflare Pages ─────────┐
                │  Vue 3 frontend            │
                └──────────────┬─────────────┘
                               │ HTTPS
                ┌──────────────▼─────────────┐
                │  AWS EC2 t3.micro          │
                │  ─────────────────────     │
                │  Go backend                │
                │  ├ HTTP API + SSE          │
                │  ├ ytmusic-search (Go)     │
                │  ├ /shift via rubberband + │
                │  │  ffmpeg CLIs            │
                │  ├ yt-dlp                  │
                │  └ Storage client (R2)     │
                │                            │
                │  bgutil PoT sidecar        │
                │  (separate process / port) │
                └──────────────┬─────────────┘
                               │ HTTPS (presigned URLs)
                ┌──────────────▼─────────────┐
                │  Hugging Face ZeroGPU      │
                │  Space — Python            │
                │  ├ /separate (Demucs)      │
                │  └ /melody   (CREPE)       │
                └────────────────────────────┘
                               │
                ┌──────────────▼─────────────┐
                │  Cloudflare R2             │
                │  cantus-cache              │
                │  (presigned URLs from Go)  │
                └────────────────────────────┘
```

Two services, one cache, one CDN. Python only exists where GPU exists.

## 5. Locked design decisions

### 5.1 Move `/search` into Go using `github.com/raitonoberu/ytmusic`

A new `backend/services/ytmusic_search.go` wraps the Go library and exposes the same behavior as today's `services.YouTubeService.Search`:

- Calls `ytmusic.Search(query)` with `SearchFilter = TrackFilter` (song-entities only, matches Python's `filter="songs"`).
- Pages via `s.Next()` (returns 20 tracks per call); accumulates until `offset+limit` results are available.
- Applies the same "non-studio" regex filter currently in `audio-processor/services/ytmusic_service.py`: drops titles whose parenthetical tags match `(live|session|acoustic|unplugged|karaoke|demo|bootleg|remix|cover|instrumental)`.
- Applies the same TTL cache (currently 600s, max 256 entries; Python `cachetools.TTLCache` → in-process Go cache with per-entry expiry).
- Maps the upstream `TrackItem` to the same wire shape today's Python returns. The Go handler then HMAC-signs each videoId via the existing `Signer` and emits the existing JSON shape — no frontend change.
- Drops items missing a videoId (defensive — same as today's Python).

`PythonYouTubeService.Search` is deleted. `YouTubeService` interface is preserved; the new in-Go search becomes the only implementation backing `Search`. The Python `/search` endpoint and its router/service/test files are deleted.

### 5.2 Move `/shift` into Go using `rubberband` + `ffmpeg` CLIs

A new `backend/services/shift.go` wraps the same toolchain pyrubberband uses. Shape:

```go
type Shifter interface {
    Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error
}

type CLIShifter struct {
    Rubberband string // path to rubberband binary, default "rubberband"
    FFmpeg     string // path to ffmpeg, default "ffmpeg"
    Runner     CommandRunner
}

func (s *CLIShifter) Shift(ctx context.Context, in, out string, semitones float64) error {
    // 1. If in is mp3, ffmpeg-decode to a scratch WAV (rubberband only reads WAV).
    // 2. rubberband -p <semitones> <wav-in> <wav-out>
    // 3. If out should be mp3, ffmpeg-encode wav-out back to mp3.
    // 4. Clean up scratch.
}
```

`PythonCPUProcessorClient.Shift` is deleted. All `/shift` call sites (job_runner.go Stage 4, preview_shift.go) switch from `cpu.Shift(inURL, outURL, semitones)` to a local `shifter.Shift(scratchIn, scratchOut, semitones)` — and crucially, the call sites flip back to reading/writing **filesystem paths** because there's no Python boundary to mint URLs for. Each caller:

1. Streams the input from `Storage.Open(inputKey)` to a scratch tempfile.
2. Calls `shifter.Shift(scratchIn, scratchOut, semitones)`.
3. Streams `scratchOut` to `Storage.Commit(outputKey, scratchOut)`.
4. Cleans up scratch.

For R2 mode this means an upload step Go now owns instead of Python (Go reads from R2 → shifts locally → uploads back to R2). For local mode it's tempfile work — same as before Plan #1.

**Note:** This is conceptually a partial reversal of Plan #2 for the `/shift` endpoint specifically. URL-handoff still applies to `/separate` and `/melody` because Python (HF Space) is a separate host. It does NOT apply to `/shift` once `/shift` lives in the same process as Storage.

### 5.3 Delete `/preview-key` entirely

The Python `/preview-key` endpoint, its router, its service, and the `CPUProcessorClient.PreviewKey` Go method are deleted. The Go `PreviewKey` HTTP handler already reads from `melody.json` directly (Plan #2 cleanup confirmed this); it never called the Python endpoint in production.

### 5.4 Rename `audio-processor/` → `audio-processor-gpu/`

The remaining Python service contains only `/separate` and `/melody` plus shared `_io_url.py` + `logging_config.py`. The new name reflects deployment intent (this is what ships to HF ZeroGPU Space).

Trim `requirements.txt` to drop unused deps: `ytmusicapi`, `pyrubberband`. Verify `librosa` and `soundfile` aren't transitively pulled in by CREPE/Demucs before deleting — if they are, leave them.

### 5.5 Drop the CPU/GPU client split in Go

Plan #2 introduced `CPUProcessorClient` (Shift + PreviewKey) and `GPUProcessorClient` (Separate + Melody). After this plan, CPU's methods all leave Go's processor surface (Shift becomes a local Go function, PreviewKey is deleted). The remaining `GPUProcessorClient` is the only processor client. **Rename it back to `ProcessorClient`** and `PythonGPUProcessorClient` → `PythonProcessorClient`. Two interfaces collapsing to one is the same churn we did in reverse during Plan #2 — accepting it because the end state is cleaner and the previous shape no longer reflects reality.

`CPU_PROCESSOR_URL` and `CPU_PROCESSOR_TIMEOUT_SECONDS` config vars are deleted. `GPU_PROCESSOR_URL` and `GPU_PROCESSOR_TIMEOUT_SECONDS` are renamed to `PROCESSOR_URL` and `PROCESSOR_TIMEOUT_SECONDS` (default 180s). `PYTHON_PROCESSOR_URL` stays as the fallback default.

### 5.6 Keep raitonoberu/ytmusic at the dependency level — don't vendor or fork yet

The library is stale but works. Risk mitigation: if it breaks in the future (caught by integration smoke tests or user reports), fork to a `cantus-org/ytmusic` repo and pin from there. Vendoring now is premature.

## 6. File-level change list

### Create

- `backend/services/ytmusic_search.go` — new in-Go YouTube Music search wrapping raitonoberu/ytmusic, with TTL cache and non-studio filter.
- `backend/services/ytmusic_search_test.go` — table-driven unit tests. Mock the upstream via a small test seam interface (`type searchPager interface { Next() (*ytmusic.SearchResult, error) }`) so unit tests don't hit YouTube.
- `backend/services/shift.go` — `Shifter` interface + `CLIShifter` impl shelling to `rubberband` + `ffmpeg`.
- `backend/services/shift_test.go` — table-driven unit tests using the existing `services.CommandRunner` pattern to mock `rubberband` and `ffmpeg` invocations.

### Modify

- `backend/services/youtube.go` — remove `PythonYouTubeService.Search` HTTP call; the `YouTubeService` interface keeps `Search` but the new in-Go implementation owns it. Keep the existing `DownloadFull` / `DownloadPreview` yt-dlp orchestration unchanged.
- `backend/services/youtube_test.go` — drop tests for the deleted HTTP-search code paths.
- `backend/services/processor_url.go` — delete `CPUProcessorClient` interface + `PythonCPUProcessorClient` type. Rename `GPUProcessorClient` → `ProcessorClient`, `PythonGPUProcessorClient` → `PythonProcessorClient`, `NewPythonGPUProcessorClient` → `NewPythonProcessorClient`. Drop the `Shift` and `PreviewKey` methods (they no longer exist on any interface). Keep `Separate` and `Melody`. Consider renaming the file to `processor.go` to match its content; non-blocking.
- `backend/services/processor_url_test.go` — drop CPU-side tests; update GPU-side tests for renamed types.
- `backend/services/job_runner.go` — Stage 4 (Shift) switches from `cpu.Shift(inURL, outURL, semitones)` to local `shifter.Shift(scratchIn, scratchOut, semitones)` with stream-from/to-Storage scaffolding. Drop `cpu CPUProcessorClient` field; add `shifter Shifter` field. Rename `gpu` field/arg to `processor`.
- `backend/services/job_runner_test.go` — drop `fakeCPUJob`; add `fakeShifter`; rename `fakeGPUJob` references to `fakeProcessor`.
- `backend/api/handlers/preview_shift.go` — both Shift blocks switch from `cpu.Shift(inURL, outURL, …)` to `shifter.Shift(scratchIn, scratchOut, …)` with the same stream-from/to-Storage scaffolding. Handler arg `cpu services.CPUProcessorClient` → `shifter services.Shifter`.
- `backend/api/handlers/preview_shift_test.go` — replace CPU fake with shifter fake; tests now stage shifted bytes via `storage.Commit` after the fake shifter is called.
- `backend/api/handlers/preview_stems.go` — arg `gpu services.GPUProcessorClient` → `processor services.ProcessorClient`.
- `backend/api/handlers/preview_stems_test.go` — fake renames.
- `backend/api/router.go` — `NewRouter` arg `cpu services.CPUProcessorClient, gpu services.GPUProcessorClient` → `processor services.ProcessorClient, shifter services.Shifter`. Wire to PreviewShift and PreviewStems.
- `backend/cmd/server/main.go` — instantiate `services.NewYTMusicSearch(...)`, `services.NewCLIShifter(...)`, the renamed `services.NewPythonProcessorClient(cfg.ProcessorURL, ...)`. Drop the CPU client.
- `backend/config/config.go` — drop `CPUProcessorURL`, `CPUProcessorTimeoutSeconds`. Rename `GPUProcessorURL`, `GPUProcessorTimeoutSeconds` → `ProcessorURL`, `ProcessorTimeoutSeconds`. Keep `PythonProcessorURL` as the fallback. Add `RubberbandPath` and `FFmpegPath` (default to `"rubberband"` and `"ffmpeg"` — found on `$PATH`).
- `backend/config/config_test.go` — table-driven updates for renames + new fields.
- `backend/.env.example` — same renames + new vars + drop deleted vars.
- `backend/go.mod` / `backend/go.sum` — add `github.com/raitonoberu/ytmusic`.
- `CLAUDE.md` — update Architecture section: two services now; Go owns search + shift; Python is GPU-only on HF Space.

### Delete

- `audio-processor/routers/search.py` (and `tests/test_search_router.py`).
- `audio-processor/routers/shift.py` (and `tests/test_shift_router.py`).
- `audio-processor/routers/preview_key.py` (and `tests/test_preview_key_router.py`).
- `audio-processor/services/ytmusic_service.py` (and `tests/test_ytmusic_service.py`).
- `audio-processor/services/pitch_service.py` (and `tests/test_pitch_service.py`).
- `audio-processor/services/preview_key_service.py` (and `tests/test_preview_key_service.py`).
- Update `audio-processor/main.py` to mount only `/separate` and `/melody` routers.

### Rename

- `audio-processor/` → `audio-processor-gpu/` (`git mv` as a single step at the end).

### Trim

- `audio-processor-gpu/requirements.txt` — remove `ytmusicapi`, `pyrubberband`. Verify `librosa` and `soundfile` aren't transitively required by `demucs`/`crepe` before removing.

## 7. Configuration matrix

| Var | Status after this plan |
|---|---|
| `PYTHON_PROCESSOR_URL` | Kept as fallback default for `PROCESSOR_URL`. Removable later. |
| `PROCESSOR_URL` | New; replaces `GPU_PROCESSOR_URL`. Defaults to `PYTHON_PROCESSOR_URL`. Points at the HF Space URL in prod, `http://localhost:8090` in dev. |
| `PROCESSOR_TIMEOUT_SECONDS` | New; replaces `GPU_PROCESSOR_TIMEOUT_SECONDS`. Default 180. |
| `CPU_PROCESSOR_URL` | **Deleted.** |
| `CPU_PROCESSOR_TIMEOUT_SECONDS` | **Deleted.** |
| `GPU_PROCESSOR_URL` | **Renamed** to `PROCESSOR_URL`. |
| `GPU_PROCESSOR_TIMEOUT_SECONDS` | **Renamed** to `PROCESSOR_TIMEOUT_SECONDS`. |
| `RUBBERBAND_PATH` | New; defaults to `"rubberband"` (must be on `$PATH`). |
| `FFMPEG_PATH` | New; defaults to `"ffmpeg"`. |
| Other R2/storage vars | Unchanged. |

## 8. Migration plan (high-level — see implementation plan for TDD'd tasks)

Atomic single-PR series consistent with Plans #1/#2. Suggested ordering:

1. **Add Go ytmusic search.** New `services/ytmusic_search.go` + tests. Update `YouTubeService.Search` to use it. Keep the Python `/search` endpoint up so we have a rollback — but the Go side no longer calls it. Verify via curl `/api/songs/search`.
2. **Delete Python `/search`** — router, service, tests.
3. **Add Go shift.** New `services/shift.go` + tests. Update `job_runner.go` Stage 4 and both Shift blocks in `preview_shift.go` to use it. Verify via curl `/api/preview-shift` and a full `/api/generate`.
4. **Delete Python `/shift`** — router, service, tests.
5. **Delete Python `/preview-key`** — router, service, tests. (Go handler already doesn't call it.)
6. **Rename Go `GPUProcessorClient` → `ProcessorClient` and delete `CPUProcessorClient`.** Config renames. Single sweeping commit.
7. **Rename `audio-processor/` → `audio-processor-gpu/`.** Trim its `requirements.txt`. Single `git mv` step + dep cleanup.
8. **Docs:** update `CLAUDE.md` Architecture section + each service's `.env.example`.

At each step both backends boot, the moved endpoint works end-to-end through Go, and tests pass.

## 9. Testing strategy

### Go

- `ytmusic_search_test.go`: table-driven. Mock the upstream HTTP via a thin test seam (the simplest path is an interface wrapping the search-call function and a fake that returns canned `*ytmusic.SearchResult` values). Verify: TTL cache hit/miss behavior, non-studio regex filter drops the right titles, pagination across two `Next()` calls, mapping to the wire shape, dropping items without videoId.
- `shift_test.go`: table-driven. Use the existing `services.CommandRunner` pattern (already used by yt-dlp) to mock `rubberband` and `ffmpeg` invocations. Verify: correct CLI args (`-p` for semitones, correct in/out paths), error propagation, MP3↔WAV transcoding chain when input/output are MP3.
- Handler/`job_runner` tests: existing structure adapted. Fakes pre-stage shifted output via `storage.Commit` inside the `Shifter` fake (same pattern used for the Python fakes in Plan #2).

### Python

- After step 7, `audio-processor-gpu/tests/` contains only the still-relevant tests: `test_io_url.py`, `test_separate_router.py`, `test_melody_router.py`, `test_demucs_service.py`, `test_melody_service.py`, `test_health.py`, `test_logging_config.py`. The deleted endpoints' tests are gone.

### Integration smoke

- Local-mode end-to-end after every other step: `search → preview → preview-shift → generate → audio → melody`. The existing curl recipe from Plan #2 Task 10 still applies; just replace any Python `/search` or `/shift` calls with confirming that the Go-side endpoints still return the same shape.

## 10. Risks and mitigations

| Risk | Mitigation |
|---|---|
| `raitonoberu/ytmusic` breaks because YouTube Music changes its web API | Library is small and self-contained; fork to a personal repo and pin from there. We verified the library works as of 2026-06-14. |
| Go shift produces audibly different output than pyrubberband | Both pipelines shell out to the same `rubberband` CLI with the same `-p <semitones>` flag. Output should be byte-identical given identical input. Smoke-test the first shifted MP3 by ear after step 3. |
| Forgetting that `/shift` no longer uses URL handoff and accidentally introducing a presigned-URL fetch from Go's own process | The handler is rewritten explicitly to use `storage.Open` + scratch tempfiles + `storage.Commit`. No `SignGet`/`SignPut` calls for the shifted output in either `job_runner` Stage 4 or `preview_shift` handlers. Reviewer should grep for them. |
| `librosa` or `soundfile` were transitively used by Demucs or CREPE; removing them breaks the GPU service | Verify before removing: install the trimmed requirements in a fresh venv and run `pytest audio-processor-gpu/tests/` to confirm everything still works. If a missing dep is detected, restore it as an explicit pin. |
| The TTL cache implementation in Go drifts from the Python behavior | Tests assert both the cache-hit shortcut and the eviction semantics matching what the prior service did (mainly: same query → cached page returned; never produces duplicates across pages because we cache the full mapped list, not per-offset slices). |
| Pre-rename of `audio-processor/` breaks open IDE tabs, scripts, or CI configs | Single atomic `git mv` step. Grep for `audio-processor[^/-]` across the repo before merging. |

## 11. Out of scope (explicitly deferred)

- HF Space `app.py` + `@spaces.GPU(duration=120)` + pre-baked weights (next plan: deployment packaging).
- Dockerfiles, EC2 user-data, R2 provisioning (next plan).
- yt-dlp PoT provider packaging on EC2.
- Anti-abuse stack.
- Frontend deployment prep.
- Removal of `PYTHON_PROCESSOR_URL` fallback.
- Forking/vendoring `raitonoberu/ytmusic`.
