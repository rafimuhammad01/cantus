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
- `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET` — from developer.spotify.com (free, Client Credentials flow)
- `DEVICE` — `cpu`, `mps` (Apple Silicon), or `cuda`

## Architecture

```
Browser (Vue 3)
  └── HTTP/SSE ──► Go :8080
                    ├── Spotify API (search metadata + 30s preview only)
                    ├── yt-dlp (YouTube audio download)
                    └── HTTP ──► Python :8090
                                  ├── CREPE (melody extraction)
                                  ├── Demucs (vocal removal)
                                  └── librosa + pyrubberband (pitch shift)
```

## Go Module

Module path: `singing-enhancement/backend`

## Important Notes

- **Demucs first run**: downloads ~1GB model weights. Subsequent runs are fast.
- **CREPE first run**: downloads model weights on first use.
- **yt-dlp**: searches YouTube by `"{track} {artist} official audio"` filtered by duration. Legal gray area for personal use — replace with licensed source before public launch.
- **tmp/ dirs**: audio working files. Both are gitignored. Files auto-deleted after 1 hour by cleanup goroutine.
- **AudioSourceProvider interface** in `backend/services/youtube.go`: swap yt-dlp for a licensed provider without changing handler code.

## Testing Endpoints

```bash
curl localhost:8080/health
curl "localhost:8080/api/songs/search?q=bohemian+rhapsody"
curl localhost:8090/health
```
