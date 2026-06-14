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

**Python 3.12 required** (not 3.13+). Reason: CREPE depends on TensorFlow, which doesn't yet ship wheels for newer Python versions. Match the proven prototype config. Install with `brew install python@3.12`, then `python3.12 -m venv .venv`. Install audio deps (first run downloads PyTorch + Demucs models — slow, ~5-10 min):

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
                                  ├── Demucs (vocal separation)
                                  └── CREPE (melody extraction from vocals stem)
```

**Search is song-entity-level, not raw YouTube.** `github.com/raitonoberu/ytmusic` (a Go drop-in for ytmusicapi) with `filter="songs"` returns one result per song (artist + album + canonical YouTube videoId), not per video. This avoids two problems with raw yt-dlp search: (1) abuse vector — handlers could be tricked into processing any YouTube video; (2) noise — same song appearing as official/lyric/live/cover variants.

**All audio handlers are HMAC-gated.** `/api/songs/search` returns `{videoId, sig}` per result; every downstream handler requires `sig` and rejects mismatches with 400. Defense in depth against direct videoId injection.

## API Endpoints

Three-stage pipeline — users iterate on the fast preview, then commit to the slow full generate. See `FLOW.md` for the full end-to-end walkthrough (what runs in browser vs Go vs Python at each stage, cache layout, cost timeline).

| Endpoint | Speed | Purpose |
|---|---|---|
| `GET /api/songs/search?q=` | ~1-2s | in-Go YouTube Music song-entity search; returns `{videoId, sig, title, artist, album, ...}` |
| `GET /api/preview/:videoId?sig=` | ~5s cold / instant warm | 30s clip, original key |
| `POST /api/preview-shift` `{ video_id, sig, semitones }` | ~1-2s cold / instant warm | 30s clip, shifted key |
| `POST /api/generate` `{ video_id, sig, semitones }` | 90-180s cold / faster with stem cache | Full pipeline, returns job_id |
| `GET /api/status/:jobId` | SSE stream | Pipeline progress (jobId is server-issued, no sig needed) |
| `GET /api/audio/:videoId/:semitones?sig=` | instant (cached) | Full instrumental MP3 |
| `GET /api/melody/:videoId/:semitones?sig=` | instant (cached) | melody.json for pitch display |

## Go Module

Module path: `cantus/backend`

## Important Notes

- **Demucs first run**: downloads ~1GB model weights. Subsequent runs are fast.
- **CREPE first run**: downloads model weights on first use.
- **raitonoberu/ytmusic for search, yt-dlp for audio**: split intentional. `github.com/raitonoberu/ytmusic` (Go library, drop-in for ytmusicapi) gives song entities + canonical YouTube videoIds in one call; yt-dlp downloads the audio for that videoId. Both gray-area ToS for personal use; both swappable for licensed sources before public launch. Use `--cookies-from-browser chrome` if yt-dlp gets rate-limited.
- **Video ID validation**: backend validates all video IDs with `^[A-Za-z0-9_-]{11}$` AND requires a valid HMAC sig before any yt-dlp call. Regex first (cheap), sig second.
- **HMAC sig flow**: `/api/songs/search` returns `{videoId, sig}`; frontend stores both and passes `sig` on every audio call. Handlers use constant-time compare (`hmac.Equal`). Rotating `VIDEO_ID_SIGNING_KEY` invalidates outstanding sigs — users would need to re-search; acceptable.
- **CREPE runs on isolated vocals** (Demucs output), NOT the full mix — CREPE is monophonic and would track bass/guitar on a full mix.
- **Semitones capped at ±12** (one octave) — covers practical singing range needs (e.g., A → D = -7 semitones). Original conservative cap of ±5 assumed full-mix shifting; since the full-song path now shifts Demucs-isolated instrumental (no vocals → no formant problem), `CLIShifter` (rubberband) can handle ±12 with only mild artifacts. Beyond ±12 the music genuinely starts sounding wrong; keep the bound.
- **Cache is permanent**: files under `tmp/cache/` are kept indefinitely. `Storage.Has()` returns true iff the file exists AND is non-zero size (zero-byte = corrupt → regenerate). Rationale: re-running Demucs/CREPE on GPU is far more expensive than storage. Phase 2 (cloud R2) will add LRU eviction once we approach the 10GB free tier; until then, simplicity wins.
- **Concurrent generate jobs dedup by videoID**: `JobRunner.Submit(videoID, semitones)` uses a `sync.Map` keyed by videoID. A second Submit for the same videoID while a job is in flight returns the existing jobID — never spawns a duplicate pipeline. Different semitones for the same videoID also dedup at this layer; the cheap Shift stage just runs again from cache once Separate is done.
- **JobStore record TTL is separate (1h)** — that cleanup applies to in-memory job status records, not cache files.
- **tmp/ dirs**: gitignored. `tmp/cache/` holds the permanent cache; other `tmp/` files are scratch working space.
- **YouTubeService interface** in `backend/services/youtube.go`: swap yt-dlp for a licensed provider without touching handler code.
- **ProcessorClient** in `backend/services/processor_url.go`: single URL-based client that talks to the Python GPU service for Separate (Demucs) and Melody (CREPE). Configured via `PROCESSOR_URL` (defaults to `PYTHON_PROCESSOR_URL`).
- **In-process audio shift** in `backend/services/shift.go`: `CLIShifter` shells to `rubberband` (+ `ffmpeg` for MP3↔WAV transcoding). Replaces the prior `/shift` Python endpoint. Stage 4 of `job_runner` and both shift paths in `preview_shift.go` stream from `Storage.Open` → tempfile → `Shifter.Shift` → `Storage.Commit`; no URL handoff needed for local-process work.
- **In-process YouTube Music search** in `backend/services/ytmusic_search.go`: wraps `github.com/raitonoberu/ytmusic` (Go drop-in for ytmusicapi). Same TTL cache (600s, 256 entries) and non-studio regex filter as before.
- **Storage interface** in `backend/services/storage.go`: handlers operate on opaque keys via `Key / Has / SignGet / SignPut / Commit / Open / Verify`. Python services receive presigned URLs (never filesystem paths); they stream-download input, process locally, then stream-upload output. `LocalDiskStorage` mints `/internal/blob/{key}` HMAC-gated URLs in local dev; `R2Storage` mints real R2 presigned URLs via `aws-sdk-go-v2`. Selected by `STORAGE_BACKEND` (`local` or `r2`).

## Testing Endpoints

```bash
curl localhost:8080/health
curl "localhost:8080/api/songs/search?q=bohemian+rhapsody"
curl localhost:8090/health
```
