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
- [x] Models: SearchResult, Job, JobStatus, ProcessRequest
- [x] HMAC signing helpers (`services/sign.go`): Signer.Sign/Valid with `hmac.Equal` constant-time compare, hex-decode rejects non-hex input
- [x] JobStore service (`services/job_store.go`): in-memory map + `sync.RWMutex` + TTL cleanup goroutine (record TTL, not cache files)
- [x] Storage interface + LocalDiskStorage (`services/storage.go`): LocalPath/Has/Commit/Open, TTL-aware cleanup goroutine + empty `{videoID}/` dir pruning
- [x] Structured logging (zerolog) + request-id middleware (`logger/logger.go`): `LOG_LEVEL` config, `X-Request-ID` response header, request-scoped logger via `FromCtx`
- [x] `cmd/server/main.go` entry point: composes config + logger + Signer + LocalDiskStorage (+ cleanup goroutine) + PythonYouTubeService + router; SIGINT/SIGTERM → graceful shutdown with 10s deadline (backfilled during Group 4 — the libraries were green but unwired)

## Group 3 — Python Microservice Foundation + Song Search
Reordered ahead of the Go search/preview group because the Go `/api/songs/search` handler proxies through Python's `ytmusicapi` — Python must exist first.
- [x] FastAPI app skeleton with `/health` endpoint
- [x] Create venv (`audio-processor/.venv/`) + `pyproject.toml` (ruff + pytest) + `requirements.txt` (Group 3 deps pinned; heavy ML deps deferred to Group 6)
- [x] Structured JSON logging via `logging_config.setup_logging()` using `pythonjsonlogger.json.JsonFormatter`
- [x] `services/ytmusic_service.py` — `SearchService` wraps `YTMusic.search(query, filter="songs", limit=N)`, maps raw → `{videoId, title, artist, album, duration_sec, thumbnail_url}`, skips entries missing `videoId`, TTLCache(maxsize=256, ttl=600), trims `mapped[:limit]` to defend against ytmusicapi v1.12.1 ignoring `limit`
- [x] `routers/search.py` — `POST /search { query, limit }`, pydantic v2 validation (query 1-200 chars, limit 1-20), `Annotated[SearchService, Depends(get_search_service)]` DI pattern (overridable via `app.dependency_overrides` in tests)
- [x] Manual integration test: `/health` returns `{"status":"ok"}`, `/search` with `query="wish you were here"` returns Pink Floyd / Neck Deep / Avril Lavigne in the top results, exactly `limit` entries returned

## Group 4 — Go Search + Preview Download
- [x] Video ID validator: regex `^[A-Za-z0-9_-]{11}$` (`services/videoid.go`) — runs on every videoId received by ANY handler.
- [x] `YouTubeService.Search(query, limit, offset)` — HTTP POST `python:8090/search`, then HMAC-sign each `videoId` and attach `sig` to each result. Drops items whose videoID fails the regex. Returns `SearchPage{Items, HasMore}`.
- [x] `YouTubeService.DownloadPreview(videoId)` — `yt-dlp --download-sections "*0-30" -x --audio-format mp3` → 30s MP3 committed via Storage to `{video_id}/preview.mp3`. `CommandRunner` interface + `ExecRunner` for testability; `--` separator before URL as defense-in-depth.
- [x] `GET /api/songs/search?q=&limit=&offset=` handler — validates q (1..200), limit (1..20), offset (>=0); 400 on validation, 502 on upstream error; nil items coerced to `[]` for JSON.
- [x] `GET /api/preview/:videoId?sig=` handler — order is load-bearing: regex → sig → Has → DownloadPreview (on miss) → http.ServeContent (Range support). Logs to request-scoped zerolog via `logger.FromCtx`.
- [x] Manual smoke test: search Bohemian Rhapsody → Queen first result; cold preview 2.95s (489KB MP3, valid ID3); warm preview ~9ms cached; tampered/missing sig → 400; 10-char videoID → 400; `tmp/cache/{videoID}/preview.mp3` written.

## Group 5 — Preview Pitch Shift (fast iteration loop)
- [x] yt-dlp preview window changed from `*0-30` → `*30-60` so preview lands past intros / inside vocals (no cost change; same DASH-range fetch).
- [x] `brew install rubberband` + `pip install soundfile pyrubberband numpy` into `audio-processor/.venv/`; pinned in `requirements.txt`; prerequisite added to `CLAUDE.md`.
- [x] Python `services/pitch_service.py` — `PitchService.shift(input, output, semitones)`: soundfile read (ffmpeg-decode fallback for MP3) → `pyrubberband.pitch_shift` → tmp WAV → ffmpeg → 128kbps MP3 → atomic `os.replace`. Idempotent if output exists. Injectable runner for tests. **Note**: tried `-F` (formant preservation) and reverted — it produces a doubling/phasing artifact on polyphonic mixes because the source-filter model assumed by rubberband doesn't fit a sum of vocals + instruments. `-F` belongs on isolated vocal stems (deferred to Phase-2 karaoke vocal-guide track in Group 7+).
- [x] Python `routers/shift.py` — `POST /shift {input_path, output_path, semitones}` (pydantic, semitones -12..+12), DI via `lru_cache`. 404 on FileNotFoundError, 500 on RuntimeError. 14 tests (incl. FFT 880Hz verification of +12 semitone shift).
- [x] Go `services/processor.go` — `ProcessorClient` interface + `PythonProcessorClient.Shift`. Round-tripper-injectable, ctx-respectful, non-2xx → wrapped error with status code.
- [x] Go `api/handlers/preview_shift.go` — `POST /api/preview-shift {video_id, sig, semitones}`. Order is load-bearing: decode → regex → sig → semitones range [-5,+5] → cache lookup `preview-shifts/{n}.mp3` → on miss ensure preview.mp3 (DownloadPreview if not cached) → tmp dir + `processor.Shift` → `storage.Commit` → `http.ServeContent` (Range support). 14 table-driven test cases + range test.
- [x] **Storage path fix discovered during smoke test**: `LocalDiskStorage` now resolves `root` to absolute via `filepath.Abs` in the constructor — required because paths cross the Go→Python service boundary and Python's CWD differs from Go's. Test added to lock in absolute-path invariant.
- [x] Manual smoke test: preview-shift -2 cold 777ms, warm 9ms; +1/+3/0 each ~700ms; 128kbps 44.1kHz stereo MP3 output. semitones=±6 → 400; bad sig/missing sig/bad videoID/malformed JSON → 400. Cache layout matches FLOW.md.

## Group 6 — Python Audio Pipeline (heavy endpoints)
Foundation, deps, and JSON logging already done in Group 3. `pitch_service.py` + `POST /shift` already done in Group 5 (pulled forward).
- [x] **Recreated venv on Python 3.12** (was 3.14; TensorFlow has no 3.14 wheels). Matches the proven prototype config.
- [x] Heavy deps installed: `torch 2.12.0`, `torchaudio 2.11.0`, `torchcodec 0.14.0` (required by torchaudio.save — discovered mid-smoke-test), `tensorflow 2.21.0`, `crepe 0.0.16`, `librosa 0.11.0`, `demucs 4.0.1`.
- [x] `pitch_service.py` + `POST /shift` — completed in Group 5.
- [x] `demucs_service.py` — **in-process** Demucs via custom `InProcessSeparator` (wraps `demucs.pretrained.get_model` + `demucs.apply.apply_model` + `demucs.audio.AudioFile`; the higher-level `demucs.api.Separator` isn't shipped in the PyPI wheel of demucs 4.0.1). Model loads once on first request via the `@lru_cache` factory and is held for the process lifetime — eliminates the ~30-60s subprocess cold start per call. Combines 4 stems into `vocals` + `no_vocals` (drums+bass+other). Writes atomically via `.tmp` + `os.replace`, output is 44.1 kHz 16-bit PCM WAV via `soundfile.write` (torchaudio 2.9+ has a TorchCodec encoding bug for WAV). 8 service tests use a duck-typed `FakeSeparator` so they exercise the production path without loading torch. **Iteration history**: started with subprocess + `htdemucs_ft + shifts=2` for quality — too slow (~5 min CPU / ~2 min MPS for 30s preview). Reverted to defaults (`htdemucs`, `shifts=1`) to keep flow velocity; the quality knobs are constructor params and can be flipped back when wiring the Phase-2 karaoke vocal-guide track.
- [x] `POST /separate {input_path, output_dir}` → `{vocals_path, no_vocals_path}`. 404 on missing input, 500 on Demucs error.
- [x] `melody_service.py` — exactly the §2 spec, lifted from prototype:
  - `librosa.load(vocals_path, sr=16000)` — librosa is load-bearing for the 44.1 → 16 kHz resample CREPE requires (see `[[feedback-librosa-required]]`).
  - `librosa.feature.rms(y=audio, frame_length=1024, hop_length=hop_len)` — energy series.
  - `crepe.predict(audio, sr, model_capacity="tiny", step_size=50, viterbi=True)` — 50 ms hop, "tiny" capacity.
  - Gate: voiced iff `conf > 0.60 AND energy > 0.015 AND freq > 0`.
  - Output JSON: `{"hop_ms": 50, "min_hz": <f>, "max_hz": <f>, "frames": [[t_ms, hz], ...]}` — unvoiced frames have `hz = 0.0`. Compact array form (~3× smaller than prototype's dict). Atomic write via `.tmp` + `os.replace`. Idempotent (re-runs on corrupted/partial JSON).
  - Both `predictor` and `loader` injected for fast unit tests (no real CREPE calls in tests). 10 service tests + 7 router tests.
- [x] `POST /melody {vocals_path, output_path}` → `{output_path}`. 404 on missing input, 500 on extraction error.
- [x] Manual smoke test on a real 30s preview (Bohemian Rhapsody): Demucs **12.5 s** cold (M-series internal parallelism), CREPE **25.7 s** cold (incl. model + viterbi init), both endpoints **9 ms** warm (idempotent). Output melody covers ~40 s, 36.8% voiced frames, min 193.88 Hz / max 464.15 Hz. First detected pitch 335.79 Hz at t=0ms aligns with F4 — matches Bohemian Rhapsody's opening note ("Is this the real life?" sits around E4–F4). Pipeline validated end-to-end.
- [ ] Stage timing logs via python-json-logger. *(Not blocking Group 7 — defer to polish.)*
- [ ] **§6 sanity check** (decision lock-in for the math-transpose architecture): on the same song, compute `voiced_set_A` (prototype path: shift vocals → CREPE+RMS on shifted) and `voiced_set_B` (plan path: CREPE+RMS on original, transpose Hz at serve). If frame-level agreement is >95%, commit to math-transpose. Otherwise re-evaluate before Group 7. *(Run this before Group 7's transpose endpoint.)*

## Group 7 — Generate Pipeline + SSE + Stem Cache
- [x] `ProcessorClient.Separate` + `.Melody` (round-tripper-tested HTTP wrappers); `YouTubeService.DownloadFull` (yt-dlp full song → `original.wav`).
- [x] `services/job_runner.go` — semaphore-bounded concurrency (env `MAX_CONCURRENT_JOBS=1`), 4-stage pipeline with per-stage cache-skip, atomic JobStore status transitions; 9 stage-orchestration tests + bounded-concurrency test.
- [x] `POST /api/generate` `{video_id, sig, semitones}` — validates, `JobRunner.Submit`, returns `{job_id}` (202).
- [x] SSE `GET /api/status/:jobId` — polls JobStore every 300ms, emits `data: {"status","message"}` events, terminates on `done`/`error`/client-disconnect.
- [x] `GET /api/audio/:videoId/:semitones?sig=` — `http.ServeContent` of `shifted/{n}/audio.mp3`; 404 on cache miss (no auto-generate).
- [x] `GET /api/melody/:videoId/:semitones?sig=` — math-transposes cached `melody.json` server-side (`hz × 2^(n/12)`); voiced/unvoiced gate preserved since `0 × ratio = 0`. Adds `key` + `transposed_key` fields (see backend key-detection task at start of Group 8 below).
- [x] Live smoke test through the full `/api/generate` flow: SSE progress runs through `downloading → separating → melody → shifting → done`, full instrumental serves clean at the user's chosen key.
- [x] **Semitones bound widened ±5 → ±12** across handlers and tests — singers need beyond ±5 (e.g. Fake Plastic Trees A → D = −7). CLAUDE.md updated with reasoning.

## Group 8 — Vue Frontend (Search + Player)
Reference: plan at `~/.claude/plans/compressed-baking-quasar.md`. UI direction approved: dark theme everywhere; homepage Google-style centered search; infinite-scroll results; player page mirrors Ultimate Guitar transpose pill `[ − Tr. n + ]` + separate "Key: A → G" line.

### Backend pre-step (done as part of Group 8)
- [x] **Python key detection** (`melody_service.py`): `estimate_key()` Krumhansl-Schmuckler on voiced-frame pitch-class histogram → adds `"key": "A major"` to `melody.json`. Empty string for all-unvoiced. 145 tests pass.
- [x] **Go key transpose** (`api/handlers/melody.go`): `/api/melody` response now includes `key` (passthrough) + `transposed_key` (computed via note-wheel math, preserves major/minor). Internal `transposeKey()` + 12-case table-driven test.

### Frontend
- [ ] Scaffold Vue 3 + TS + Vue Router + Pinia + Tailwind CSS v4 (via `@tailwindcss/vite`). Vite proxy `/api` → `:8080`. Set page title "Cantus". Dark theme defaults (`#0f0d14` background). Strip scaffold demo content (HelloWorld, TheWelcome, counter store, etc.) leaving clean `App.vue` + empty placeholder views.
- [ ] `services/api.ts` typed wrappers for all 7 endpoints (search, preview URL, previewShift Blob, generate, getMelody, audio URL, status SSE).
- [ ] `composables/useSSE.ts` — EventSource wrapper, auto-close on `done`/`error`, cleanup on unmount.
- [ ] `stores/search.ts` (query/results/hasMore/offset/loading/error + runSearch/loadMore) and `stores/player.ts` (videoId/sig/song/semitones/audioSrc/originalKey/transposedKey/jobId/jobStatus/melody + loadPreview/setSemitones/generateFullSong).
- [ ] `SearchView.vue` + `SearchBar.vue` (input + submit) + `SongCard.vue` (thumb/title/artist/album/duration → routes to player). Infinite scroll via IntersectionObserver sentinel.
- [ ] `PlayerView.vue` with UG-style layout: song header, `[▶ Play]` + transpose pill, `Key: A → G` line (hidden until melody loaded; short-form letter+m rendering), seek bar, "Generate Full Song" button → `ProcessingStatus.vue` SSE progress.
- [ ] `KeySelector.vue` — rounded pill `[ − ] Tr. {n} [ + ]`, clamps ±12, emits change → player store fires `/api/preview-shift` + refetches `/api/melody`.
- [ ] `AudioPlayer.vue` — `<audio>` wrapper. Source state machine: preview URL → blob URL (after preview-shift) → full audio URL (after generate done).
- [ ] End-to-end smoke test: search "fake plastic trees" → click result → preview plays → drag pill to −7 → shifted preview + "Key: A → D" updates → "Generate Full Song" → SSE progress → full instrumental plays cleanly.

## Group 9 — Pitch Detection
- [x] `usePitchDetection.ts` composable — **AnalyserNode + rAF + pitchy (McLeod method)**. AudioWorklet was the original plan but reverted for v1: pitchy on the main thread at rAF is ~0.3 ms/call on M-series; worklet's cross-thread postMessage cost outweighs its jitter win at our 30 ms latency budget. Constraints: `echoCancellation/noiseSuppression/autoGainControl: false`, `latencyHint: 'interactive'`, `fftSize: 2048`, `smoothingTimeConstant: 0`. Injectable seams: `audioContextFactory`, `getUserMediaFn`, `filter` for tests.
- [x] **Filter chain on raw pitch** — `utils/pitchFilter.ts`, 50 table-driven tests; ported from prototype `audio_renderer.py:71-113`. Split-output `step()` returns `{ filteredMidi, smoothedMidi }` so jump-rejection compares against post-gate (not post-median) values. Constants:
  - `CONF_THRESHOLD=0.5`, `HZ_LOW=60`, `HZ_HIGH=1000`
  - Target-proximity gate: reject when `diff > 7 AND |diff - 12| > 3` (conjunction — keeps fold candidates 9-15 alive).
  - Octave-fold: if `diff ∈ [9, 15]`, fold raw ±12 toward target.
  - **Jump rejection: `JUMP_SEMITONES = 24`** (matches prototype line 44; the "8" in the original Group 9 spec was stale).
  - 9-frame nan-median smoothing.
  - **Silence reset: `SILENCE_RESET_FRAMES = 94`** — scaled from prototype's 65 (at ~23 ms/frame) to preserve ~1.5 s semantics at ~16 ms/frame rAF cadence.
- [x] Pinia pitch store (`stores/pitch.ts`) — `userTimes/userMidis` parallel arrays + `frameHits/frameTotal/currentMidi/isActive` + `hitRate` (null until `frameTotal > 30`). `trimSinceSeek(t)` does NOT reset hits/total (matches prototype `_seek`). 24 tests.
- [ ] `PitchMeter.vue` — current note name + cents off. *(Deferred — `PitchDiagram` already conveys current pitch via the user-line trail + score; a standalone meter feels redundant in this UI. Can revisit if Group 9 smoke testing surfaces the need.)*
- [x] `PitchDiagram.vue` — scrolling SVG, 10s window centered on `audio.currentTime`. Past target line blue `#1f77b4`, future gray `#aaaaaa`. User line color-coded: green ≤0.5 st, yellow ≤1.5 st, red >1.5 st, orange singing-with-no-target. Red center cursor. Hybrid render: SVG for axes+target, segment-per-pair for user line (sliced to visible window before render). `ResizeObserver` for responsive width.
- [x] Hit-rate score: top-right of the diagram, rendered only when `hitRate !== null` (frameTotal > 30 gate).
- [x] Y-axis as note names — `midiToNoteName` (port of `pitch_utils.midi_to_note_name`) on integer MIDI ticks from `[yMin, yMax] = [floor(minTargetMidi)-1, ceil(maxTargetMidi)+1]`.
- [x] Integrated via `audio.currentTime` (not `performance.now`) — `AudioPlayer.vue` exposes its `<audio>` ref via `defineExpose({ audio })`; `PitchDiagram` reads `props.audioEl.currentTime` on every rAF tick. `seeked` (past tense; `seeking` fires repeatedly during scrub) wires to `pitchStore.trimSinceSeek`.
- [x] One-time headphones tooltip — localStorage key `cantus_headphones_seen`; auto-hides after 6 s on first successful mic permission.
- [ ] End-to-end test: sing into mic, verify diagram + feedback. *(Requires browser + mic — user-driven.)*

## Phase-2 / Public Launch Hardening (deferred — Cantus is currently single-user prototype scope)

### Security
HMAC sig is currently a deterministic *proof of provenance* (HMAC(key, videoID)), defending against arbitrary-videoID injection only. Before public launch, layer on:
- [ ] **Sig expiry**: `sig = HMAC(key, videoID + "|" + exp_unix)`. Search response returns `(videoId, sig, exp)`; handler checks both. Caching stays correct per (videoID, exp-window) since exp is bucketed to e.g. 1h boundaries.
- [ ] **Rate limiting** keyed on IP (and later user ID once auth exists). Chi middleware or a thin token-bucket package; bypass for `/health`.
- [ ] **Origin / CSRF** checks: only accept requests with a same-origin `Origin` header on POST/state-changing routes.
- [ ] **Sig leakage audit**: never log full sigs; truncate to first 8 hex chars. Same for videoIDs in user-facing error messages (already done in current handlers, lock in with a logging helper).
- [ ] **Key rotation runbook**: documented procedure for rotating `VIDEO_ID_SIGNING_KEY` (acknowledged as a full-flush operation in `CLAUDE.md`; runbook should make the user-impact + frontend re-search behavior explicit).

### Karaoke vocal-guide toggle
Lets the user choose to hear the original vocalist as a guide track over the shifted instrumental (Smule / Karafun pattern). Backend additions are cheap since Demucs already produces the vocals stem; the rest is a frontend feature.
- [ ] **Shift the vocals stem** in the Group 7 generate pipeline with formant preservation: `pyrubberband.pitch_shift(vocals, sr, semitones, rbargs={'-F': ''})` — `-F` works well here because the stem is isolated (single source-filter fit), so no chipmunk effect. Cache to `shifted/{n}/vocals.mp3` next to the existing `shifted/{n}/instrumental.mp3`.
- [ ] **Serve endpoint**: `GET /api/vocals/:videoId/:semitones?sig=` mirroring `/api/audio/...` — same regex + sig + Storage + http.ServeContent pattern.
- [ ] **Frontend mixing**: two `<audio>` elements (or Web Audio API gain nodes) playing in sync; vocal-volume slider controls the vocals gain. Lazy-load the vocals MP3 only when the toggle is enabled.
- [ ] **UX**: small "🎤 vocal guide" toggle + volume slider in `PlayerView.vue`. Default OFF (matches the "I'm here to sing, not listen" intent).

## Post-MVP Improvement Backlog
Captured during MVP smoke-testing. Not blockers — ordered by user-chosen priority.

### Priority 1 — Preview parity with full-song flow ✅
Shipped: `POST /api/preview-stems` runs Demucs + ffmpeg + CREPE on the 30s clip (idempotent, ~14s warm / ~50s cold); `GET /api/preview-audio` serves the clean instrumental stem; `GET /api/preview-melody/:videoId/:semitones` math-transposes the preview melody (same hz × 2^(n/12) helper as full-song melody, factored out for reuse). `POST /api/preview-shift` prefers the stem when cached and falls back to the legacy full-mix path during cache rollover. `PreviewView` mounts → fires `loadPreviewStems()`, shows a `ProcessingStatus` card during the ~14s wait, then reveals the audio + transpose pill + `PitchDiagram` together (Approach 1 — block on mount). KeySelector disabled until stems ready to prevent chipmunk shifts.
- [x] **Instrumental-only preview** — `preview-stems/no_vocals.wav` + transcoded `no_vocals.mp3` cached per song; shifted versions go to `preview-stems/shifted/{n}.mp3` (clean), with legacy `preview-shifts/{n}.mp3` (full-mix) kept as fallback for backwards-compatible cache rollover.
- [x] **Pitch detection on preview** — same CREPE pipeline produces `preview-stems/melody.json`, served math-transposed via `GET /api/preview-melody`.
- [x] **PitchDiagram on PreviewView** — same component, same `audio.currentTime` time source, same mic + filter chain. Preview and full-song now share identical singing-feedback UX.
- [ ] Browser smoke test — search → preview → confirm ~14s wait + clean instrumental + diagram → transpose → confirm fast iteration loop. *(User-driven; needs mic + browser.)*

### Priority 2 — Real-time lyrics (karaoke-style)
- [ ] Lyrics source: investigate options — `lyricsgenius` (Genius API, requires token), `syrics` (Spotify time-synced), or `musixmatch` (paid). Time-synced (LRC format) is mandatory; plain lyrics aren't useful for a karaoke flow.
- [ ] Caching: extend the per-`videoId` cache layout (e.g. `tmp/cache/{videoID}/lyrics.lrc`) with TTL aligned to the existing `CACHE_TTL_HOURS`. Lyrics are tiny, so this is essentially free storage.
- [ ] Backend endpoint: `GET /api/lyrics/:videoId?sig=` returning the parsed LRC or `404` if unavailable (some songs won't have a time-synced source).
- [ ] Frontend: a `LyricsPanel.vue` above or below the pitch diagram that highlights the active line (and ideally per-word position if the source supports it). Bind to `audio.currentTime` the same way `PitchDiagram` does.
- [ ] UX gotcha: lyrics may be unavailable for some songs — design the empty state gracefully (hide the panel, don't show a broken header).
