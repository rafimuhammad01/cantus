# Task Tracker

Full task breakdown for the cantus project. Work through groups in order — each group should be fully functional before starting the next.

Check off tasks as you complete them. When starting a Claude Code session, tell Claude which group you're working on.

## Development Workflow (TDD + Multi-Agent)
Each feature: **Test Agent (red) → Implement (green) → Refactor → repeat** per behavior.
When all group todos are done: **Code Review Agent → pre-commit hooks → commit**.

## Model Strategy
- Planning sessions → **Opus** (`/model`)
- TDD cycles → **Sonnet** (`/model`)
- Code Review Agent → **Opus** (`/model`)

## Core UX Decision (drives architecture)
Users iterate on the **30s preview** (fast, ~1-2s per key) to find the right key. Only commit to the **slow full-song pipeline** (90-180s) when they're happy with the key choice.

- `/api/preview/:videoId` — 30s clip, original key (~5s, cached)
- `/api/preview-shift` { video_id, semitones } — 30s clip pitch-shifted (~1-2s, cached per semitone)
- `/api/generate` { video_id, semitones } — full pipeline with smart caching

---

## Group 1 — Project Setup
- [x] Create CLAUDE.md (run commands, env setup, prerequisite installs)
- [x] Scaffold directory structure (backend/, audio-processor/, frontend/)
- [x] Create per-service .env.example files (backend/ and audio-processor/)
- [x] Update .gitignore
- [x] Set up pre-commit framework: create `.pre-commit-config.yaml`, run `pre-commit install`
- [x] Install Go linting: `brew install golangci-lint` (ruff/black managed by pre-commit — no separate install needed)
- [ ] `npm i -D eslint prettier` in frontend/ (deferred to Group 8 when Vue project exists)
- [x] `brew install yt-dlp ffmpeg` (prerequisite for backend + audio processor)
- [x] Remove Spotify references from CLAUDE.md and .env.example (Spotify was dropped)

## Group 2 — Go Backend Foundation
- [x] Initialize Go module (`go mod init cantus/backend`)
- [x] Chi router with CORS middleware (env-configurable origins) and /health endpoint
- [x] Config loading from .env (`os.Getenv`, fail-fast on missing required vars)
- [ ] Models: SearchResult, Job, JobStatus, ProcessRequest
- [ ] HMAC signing helpers (`services/sign.go`): sign/validSig with constant-time compare, key loaded from config
- [ ] JobStore service (in-memory map + `sync.RWMutex` + 1hr cleanup goroutine — for job records, not cache files)
- [ ] Storage interface + LocalDiskStorage (`services/storage.go`): LocalPath/Has/Commit/Open, TTL-aware cleanup goroutine
- [ ] Structured logging setup with zerolog, request-id middleware

## Group 3 — YouTube Search + Preview Download
- [ ] `YouTubeService.Search(query)` — yt-dlp `--dump-json --flat-playlist ytsearch10:` → `[]SearchResult`
- [ ] `YouTubeService.DownloadPreview(videoId)` — yt-dlp `--download-sections "*0-30"` → 30s MP3 cached at `tmp/cache/{video_id}/preview.mp3`
- [ ] Video ID validator: regex `^[A-Za-z0-9_-]{11}$`
- [ ] `GET /api/songs/search?q=` handler
- [ ] `GET /api/preview/:videoId` handler (serves cached preview.mp3)
- [ ] Manual test: search + preview a known song

## Group 4 — Python Microservice (Foundation)
- [ ] FastAPI app with `/health` endpoint
- [ ] Install Python deps: `fastapi uvicorn demucs crepe librosa pyrubberband soundfile python-json-logger`
- [ ] Structured JSON logging setup
- [ ] Manual test: `curl localhost:8090/health`

## Group 5 — Preview Pitch Shift (fast iteration loop)
- [ ] `POST /api/preview-shift` { video_id, semitones } handler
- [ ] Cache lookup: serve `tmp/cache/{video_id}/preview-shifts/{semitones}.mp3` if exists
- [ ] On cache miss: ensure preview.mp3 exists, call Python `/shift` on it
- [ ] Validate semitones range (-5 to +5)
- [ ] Manual test: preview-shift through several semitones, verify ~1-2s response

## Group 6 — Python Audio Pipeline (three endpoints)
- [ ] `pitch_service.py` — librosa + pyrubberband + ffmpeg → 128kbps MP3 (shared for preview and full song)
- [ ] `demucs_service.py` — `--two-stems vocals` → vocals.wav + no_vocals.wav
- [ ] `melody_service.py` — CREPE on **vocals.wav** (isolated), outputs melody.json (array tuple format, 30ms hop, min_hz/max_hz, original key)
- [ ] `POST /shift` endpoint (light)
- [ ] `POST /separate` endpoint (heavy)
- [ ] `POST /melody` endpoint (heavy)
- [ ] Stage timing logs via python-json-logger

## Group 7 — Generate Pipeline + SSE + Stem Cache
- [ ] `ProcessorClient` in Go: `Shift(in, semitones, out)`, `Separate(in, outDir)`, `Melody(vocals, out)` methods
- [ ] Worker pool with bounded concurrency (env `MAX_CONCURRENT_JOBS=1`)
- [ ] `POST /api/generate` { video_id, semitones } handler:
  - Returns immediately with job_id
  - Goroutine runs pipeline with smart caching: skip yt-dlp full / Demucs / CREPE / shift / transcode if cached
- [ ] SSE `GET /api/status/:jobId` with queue_position
- [ ] `GET /api/audio/:videoId/:semitones` — MP3 via http.ServeFile (Range support)
- [ ] `GET /api/melody/:videoId/:semitones` — server transposes cached original melody by semitones
- [ ] End-to-end test: cold generate (90-180s) → repeat (instant) → same video new semitones (5-15s)

## Group 8 — Vue Frontend (Search + Player)
- [ ] Create Vue 3 project (`npm create vue@latest` — TypeScript, Router, Pinia)
- [ ] Install: Tailwind CSS, **pitchy** (not pitchfinder)
- [ ] Vite proxy config (`/api` → `localhost:8080`)
- [ ] Typed API client (`services/api.ts`)
- [ ] Pinia search store + `SearchView.vue` + `SearchBar.vue` + `SongCard.vue` (click → navigates to player)
- [ ] Pinia player store + `PlayerView.vue`:
  - On mount: fires `/api/preview/:videoId` → plays in original key
  - KeySelector change: fires `/api/preview-shift` → audio reloads
  - "Generate Full Song" button: fires `/api/generate` → progress → full audio plays
- [ ] `KeySelector.vue` — semitone picker (-5 to +5)
- [ ] `AudioPlayer.vue` — `<audio>` wrapper, src swaps between preview and full track
- [ ] `ProcessingStatus.vue` — SSE progress for `/api/generate`
- [ ] `useSSE.ts` with reconnect + polling fallback
- [ ] "Start Singing" enabled only after `/api/generate` done (need melody.json)

## Group 9 — Pitch Detection
- [ ] `usePitchDetection.ts` composable — AudioWorklet + **pitchy (McLeod method)**
- [ ] Pinia pitch store (`stores/pitch.ts`)
- [ ] `PitchMeter.vue` — current note name + cents off
- [ ] `PitchDiagram.vue` — scrolling SVG: blue target line + colored user dot
- [ ] Integrate melody.json: compare live pitch using **`audio.currentTime`** (not performance.now)
- [ ] One-time headphones tooltip on mic permission prompt
- [ ] End-to-end test: sing into mic, verify diagram + feedback
