# singing-enhancement

A singing practice web app: search any song, transpose to your vocal range, hear an instrumental (vocals removed), and get real-time pitch feedback while you sing.

## Services

Three services run simultaneously in development:

```bash
# 1. Go backend (port 8080)
cd backend && go run ./...

# 2. Python audio microservice (port 8090)
cd audio-processor && uvicorn main:app --reload --port 8090

# 3. Vue frontend (port 5173)
cd frontend && npm run dev
```

## Prerequisites

Install these before anything else:

```bash
brew install yt-dlp ffmpeg
```

Python 3.11+ required. Install audio deps (first run downloads PyTorch + Demucs models — slow, ~5-10 min):

```bash
cd audio-processor && pip install -r requirements.txt
```

Go 1.22+ required:

```bash
cd backend && go mod tidy
```

## Environment Setup

Each service has its own `.env.example`. Copy and fill in before running:

```bash
cp backend/.env.example backend/.env
cp audio-processor/.env.example audio-processor/.env
```

Required vars:
- `DEVICE` — `cpu`, `mps` (Apple Silicon), or `cuda`
- All other vars have sensible defaults in `.env.example`

## Architecture

```
Browser (Vue 3)
  └── HTTP/SSE ──► Go :8080
                    ├── yt-dlp (YouTube search + audio download)
                    └── HTTP ──► Python :8090
                                  ├── Demucs (vocal separation)
                                  ├── CREPE (melody extraction from vocals stem)
                                  └── librosa + pyrubberband (pitch shift)
```

## API Endpoints

Three-stage pipeline — users iterate on the fast preview, then commit to the slow full generate:

| Endpoint | Speed | Purpose |
|---|---|---|
| `GET /api/songs/search?q=` | ~2s | yt-dlp YouTube search results |
| `GET /api/preview/:videoId` | ~5s cold / instant warm | 30s clip, original key |
| `POST /api/preview-shift` `{ video_id, semitones }` | ~1-2s cold / instant warm | 30s clip, shifted key |
| `POST /api/generate` `{ video_id, semitones }` | 90-180s cold / faster with stem cache | Full pipeline, returns job_id |
| `GET /api/status/:jobId` | SSE stream | Pipeline progress |
| `GET /api/audio/:videoId/:semitones` | instant (cached) | Full instrumental MP3 |
| `GET /api/melody/:videoId/:semitones` | instant (cached) | melody.json for pitch display |

## Go Module

Module path: `singing-enhancement/backend`

## Important Notes

- **Demucs first run**: downloads ~1GB model weights. Subsequent runs are fast.
- **CREPE first run**: downloads model weights on first use.
- **yt-dlp**: handles both search and audio download — no Spotify or other API key needed. Legal gray area for personal use; replace with a licensed source before public launch. Use `--cookies-from-browser chrome` if yt-dlp gets rate-limited.
- **Video ID validation**: backend validates all video IDs with `^[A-Za-z0-9_-]{11}$` before any yt-dlp call.
- **CREPE runs on isolated vocals** (Demucs output), NOT the full mix — CREPE is monophonic and would track bass/guitar on a full mix.
- **Semitones capped at ±5** — pyrubberband artifacts become audible beyond that range.
- **tmp/ dirs**: audio working files. Both are gitignored. Files auto-deleted after 1 hour by cleanup goroutine.
- **YouTubeService interface** in `backend/services/youtube.go`: swap yt-dlp for a licensed provider without touching handler code.

## Testing Endpoints

```bash
curl localhost:8080/health
curl "localhost:8080/api/songs/search?q=bohemian+rhapsody"
curl localhost:8090/health
```
