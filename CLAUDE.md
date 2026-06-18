# cantus

A singing practice web app: search any song, transpose to your vocal range, hear an instrumental (vocals removed), and get real-time pitch feedback while you sing.

## Services

Three services run simultaneously in development:

```bash
# 1. Go backend (port 8080)
cd backend && go run ./...

# 2. Python audio microservice (port 8090)
cd audio-processor-gpu && uvicorn main:app --reload --port 8090

# 3. Vue frontend (port 5173)
cd frontend && npm run dev
```

## Prerequisites

Install these before anything else:

```bash
brew install yt-dlp ffmpeg rubberband
```

`rubberband` is the CLI that `CLIShifter` (Go) shells out to for pitch shifting; `ffmpeg` handles MP3↔WAV transcoding around it.

**Python 3.12 required** (not 3.13+). Reason: CREPE depends on TensorFlow, which doesn't yet ship wheels for newer Python versions. Match the proven prototype config. Install with `brew install python@3.12`, then `python3.12 -m venv .venv`. Install audio deps (first run downloads PyTorch — slow, ~5-10 min; BS-Roformer weights are seeded separately via `modal run seed_models.py`):

```bash
cd audio-processor-gpu && pip install -r requirements.txt
```

Go 1.22+ required:

```bash
cd backend && go mod tidy
```

## Environment Setup

Each service has its own `.env.example`. Copy and fill in before running:

```bash
cp backend/.env.example backend/.env
cp audio-processor-gpu/.env.example audio-processor-gpu/.env
```

Required vars:
- `DEVICE` — `cpu`, `mps` (Apple Silicon), or `cuda`
- `VIDEO_ID_SIGNING_KEY` — 32+ random bytes used to HMAC-sign videoIds. Generate with `openssl rand -hex 32`. Never commit. Backend fails to start if missing or < 32 bytes.
- All other vars have sensible defaults in `.env.example`

## Architecture

```
Browser (Vue 3)
  └── HTTP/SSE ──► Go :8080
                    ├── raitonoberu/ytmusic (song-entity search → canonical videoId)
                    ├── yt-dlp (audio download by videoId — NOT used for search)
                    ├── rubberband + ffmpeg (in-process CLI pitch shift)
                    └── HTTP ──► Python :8090
                                  ├── BS-Roformer (vocal separation)
                                  └── CREPE (melody extraction from vocals stem)
```

**Search is song-entity-level, not raw YouTube.** `github.com/raitonoberu/ytmusic` (a Go drop-in for ytmusicapi) with `filter="songs"` returns one result per song (artist + album + canonical YouTube videoId), not per video. This avoids two problems with raw yt-dlp search: (1) abuse vector — handlers could be tricked into processing any YouTube video; (2) noise — same song appearing as official/lyric/live/cover variants.

**All audio handlers are HMAC-gated.** `/api/songs/search` returns `{videoId, sig}` per result; every downstream handler requires `sig` and rejects mismatches with 400. Defense in depth against direct videoId injection.

## API Endpoints

Three-stage pipeline — users iterate on the fast preview, then commit to the slow full generate. See `FLOW.md` for the full end-to-end walkthrough (what runs in browser vs Go vs Python at each stage, cache layout, cost timeline).

| Endpoint | Speed | Purpose |
|---|---|---|
| `GET /health` | instant | Liveness probe; returns `{"status":"ok"}` |
| `GET /api/songs/search?q=` | ~1-2s | in-Go YouTube Music song-entity search; returns `{videoId, sig, title, artist, album, ...}` |
| `GET /api/preview/:videoId?sig=` | ~5s cold / instant warm | 30s clip, original key, WITH vocals |
| `GET /api/preview-key/:videoId?sig=` | instant (cached) | Song key from full-pipeline `melody.json`; returns `{"key":""}` until generate has run |
| `POST /api/preview-shift` `{ video_id, sig, semitones }` | ~1-2s cold / instant warm | 30s clip, shifted key, WITH vocals |
| `POST /api/preview-stems` `{ video_id, sig }` | ~30-60s cold / instant warm | Run BS-Roformer + CREPE on 30s clip; produces clean instrumental + melody for preview; returns `{ready:true}` |
| `GET /api/preview-audio/:videoId?sig=` | instant (cached) | 30s clean instrumental MP3 (original key); requires preview-stems to have run |
| `GET /api/preview-melody/:videoId/:semitones?sig=` | instant (cached) | Math-transposed melody from preview-stems pipeline |
| `POST /api/prewarm` `{ video_id, sig }` | returns immediately (202) | Fire-and-forget: runs stages 1–3 (download + separate + melody) in background; returns `{job_id}`; frontend calls on PreviewView mount |
| `POST /api/generate` `{ video_id, sig, semitones }` | 90-180s cold / fast if prewarm ran | Full pipeline, returns `{job_id}`; awaits in-flight prewarm then runs shift |
| `GET /api/status/:jobId` | SSE stream | Pipeline progress (jobId is server-issued, no sig needed) |
| `GET /api/audio/:videoId/:semitones?sig=` | instant (cached) | Full instrumental MP3 |
| `GET /api/melody/:videoId/:semitones?sig=` | instant (cached) | melody.json for pitch display, math-transposed to requested semitones |
| `GET /api/lyrics/:videoId?lyrics_sig=&title=&artist=&album=&duration_sec=` | ~1s / instant warm | Timed lyrics from LRCLIB; cached; returns `{available:false}` on miss |

## Go Module

Module path: `cantus/backend`

## Important Notes

- **BS-Roformer ckpt**: ~640 MB, seeded into the Modal Volume once via `modal run seed_models.py`. Subsequent cold starts read from the Volume (~5–10s). Local runs read from `$MODEL_DIR` (default `./tmp/models`).
- **CREPE first run**: downloads model weights on first use.
- **raitonoberu/ytmusic for search, yt-dlp for audio**: split intentional. `github.com/raitonoberu/ytmusic` (Go library, drop-in for ytmusicapi) gives song entities + canonical YouTube videoIds in one call; yt-dlp downloads the audio for that videoId. Both gray-area ToS for personal use; both swappable for licensed sources before public launch. Use `--cookies-from-browser chrome` if yt-dlp gets rate-limited.
- **Video ID validation**: backend validates all video IDs with `^[A-Za-z0-9_-]{11}$` AND requires a valid HMAC sig before any yt-dlp call. Regex first (cheap), sig second.
- **HMAC sig flow**: `/api/songs/search` returns `{videoId, sig}`; frontend stores both and passes `sig` on every audio call. Handlers use constant-time compare (`hmac.Equal`). Rotating `VIDEO_ID_SIGNING_KEY` invalidates outstanding sigs — users would need to re-search; acceptable.
- **CREPE runs on isolated vocals** (BS-Roformer output), NOT the full mix — CREPE is monophonic and would track bass/guitar on a full mix.
- **Semitones capped at ±12** (one octave) — covers practical singing range needs (e.g., A → D = -7 semitones). Original conservative cap of ±5 assumed full-mix shifting; since the full-song path now shifts BS-Roformer-isolated instrumental (no vocals → no formant problem), `CLIShifter` (rubberband) can handle ±12 with only mild artifacts. Beyond ±12 the music genuinely starts sounding wrong; keep the bound.
- **Cache is permanent**: files under `tmp/cache/` are kept indefinitely. `LocalDiskStorage` has no TTL enforcement and no cleanup goroutine. `Storage.Has()` returns true iff the file exists AND is non-zero size (zero-byte = corrupt → regenerate). Rationale: re-running BS-Roformer/CREPE on GPU is far more expensive than storage. Cloud deployment uses R2 with a bucket lifecycle policy for eventual eviction.
- **Two dedup maps in JobRunner**: `prewarmInflight` keyed by `videoID` (for `SubmitPrewarm`), and `shiftInflight` keyed by `videoID|semitones` (for `Submit`). A `Submit` call awaits an in-flight prewarm via a `done` channel before running stage 4. Different semitones for the same videoID each get their own shift job (no dedup across semitones).
- **JobStore record TTL is separate (1h)** — that cleanup applies to in-memory job status records, not cache files.
- **VideoFailureTracker** — per-videoID circuit breaker in `services/job_runner.go`. After `maxFailuresPerVideo=3` consecutive failures, the videoID is blocked for `failureCooldown=30min`. Both `SubmitPrewarm` and `Submit` call `IsBlocked` before doing any work and short-circuit to a `StatusError` job. The `PreviewStems` handler has its own independent tracker instance (passed via `NewRouter`).
- **Per-stage retry** — every external/IO call in the pipeline (download, separate, melody, shift, and the same three in preview-stems) is wrapped with `Retry(ctx, PipelineRetryAttempts=3, PipelineRetryBaseDelay=2s, ...)` with exponential backoff. Constants live in `services/job_runner.go`.
- **Prewarm trigger** — `PreviewView.vue` fires `POST /api/prewarm` via `player.startPrewarm` inside `onMounted` as fire-and-forget (non-fatal on error). This means stages 1–3 (download + separate + melody) are running in the background while the user picks their key; when they click "Practice Full Song", `generate` only has to run the cheap shift stage.
- **tmp/ dirs**: gitignored. `tmp/cache/` holds the permanent cache; other `tmp/` files are scratch working space.
- **YouTubeService interface** in `backend/services/youtube.go`: swap yt-dlp for a licensed provider without touching handler code.
- **ProcessorClient** in `backend/services/processor_url.go`: single URL-based client that talks to the Python GPU service for Separate (BS-Roformer) and Melody (CREPE). Configured via `PROCESSOR_URL` (defaults to `PYTHON_PROCESSOR_URL`).
- **In-process audio shift** in `backend/services/shift.go`: `CLIShifter` shells to `rubberband` (+ `ffmpeg` for MP3↔WAV transcoding). Replaces the prior `/shift` Python endpoint. Stage 4 of `job_runner` and both shift paths in `preview_shift.go` stream from `Storage.Open` → tempfile → `Shifter.Shift` → `Storage.Commit`; no URL handoff needed for local-process work.
- **In-process YouTube Music search** in `backend/services/ytmusic_search.go`: wraps `github.com/raitonoberu/ytmusic` (Go drop-in for ytmusicapi). Same TTL cache (600s, 256 entries) and non-studio regex filter as before.
- **Storage interface** in `backend/services/storage.go`: handlers operate on opaque keys via `Key / Has / SignGet / SignPut / Commit / Open / Verify`. Python services receive presigned URLs (never filesystem paths); they stream-download input, process locally, then stream-upload output. `LocalDiskStorage` mints `/internal/blob/{key}` HMAC-gated URLs in local dev; `R2Storage` mints real R2 presigned URLs via `aws-sdk-go-v2`. Selected by `STORAGE_BACKEND` (`local` or `r2`).

## Testing Endpoints

```bash
curl localhost:8080/health
curl "localhost:8080/api/songs/search?q=bohemian+rhapsody"
curl localhost:8090/health
```
