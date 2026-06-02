# End-to-End Flow

How a user goes from typing a search query to singing along, and which service does what at each step. Read this alongside `CLAUDE.md` (architecture) and `TASKS.md` (build order).

The flow has **five stages**. The first three are cheap and fast — they exist so the user can find the right song and the right key without paying the cost of Demucs. Stage 4 is the slow commit. Stage 5 is the singing experience.

**Two things to know upfront:**

1. **Search is via ytmusicapi (YouTube Music), not raw YouTube.** This gives song-entity-level results (one row per song, with artist + album metadata) instead of arbitrary YouTube videos. ytmusicapi lives in the Python service.
2. **All audio handlers require an HMAC sig** issued by `/api/songs/search`. The browser stores `(videoId, sig)` together and passes both on every subsequent call. A direct hit to `/api/preview/:videoId` without a valid sig returns 400 — handlers can only process videoIds that came from a real search.

---

## Stage 1 — Search (~1-2s, no audio yet) — ytmusicapi song entities

```
Browser ──GET /api/songs/search?q=wish+you+were+here──► Go :8080
                                                          │
                                                          ├── HTTP ──► Python :8090
                                                          │              POST /search { query, limit:10 }
                                                          │              │
                                                          │              └── ytmusicapi
                                                          │                  YTMusic().search(query, filter="songs", limit=10)
                                                          │              ◄── [{videoId, title, artists[], album,
                                                          │                    duration, thumbnails[]}, ...]
                                                          │
                                                          └── HMAC-sign each videoId
                                                              sig = HMAC-SHA256(VIDEO_ID_SIGNING_KEY, videoId)
Browser ◄──── [{video_id, sig, title, artist, album, duration_sec, thumbnail_url}, ...]
```

`SearchView.vue` renders results as `SongCard.vue`. Because `filter="songs"` is set on the Python side, results are **canonical song entities** — one row per song. Searching *"wish you were here"* yields Pink Floyd, Neck Deep, Avril Lavigne as distinct rows, NOT three variants of the same song. Lyric videos, live versions, covers, and non-music videos are excluded by the YouTube Music catalog itself.

`(videoId, sig)` pairs are stored together in the Pinia search store, then handed to the player store when the user picks a card. The `sig` is required for every subsequent audio call.

---

## Stage 2 — Land on player (Preview, ~5s cold, instant warm)

Clicking a card routes to `/player/:videoId` and hands `(videoId, sig)` to the player store. `PlayerView.vue` mounts and fires:

```
Browser ──GET /api/preview/fJ9rUzIMcZQ?sig=<HMAC>──► Go :8080
                                                      │
                                                      ├── validate videoId: ^[A-Za-z0-9_-]{11}$
                                                      ├── validSig(videoId, sig)  ← 400 if mismatch
                                                      ├── check tmp/cache/fJ9rUzIMcZQ/preview.mp3
                                                      │     hit?  serve immediately
                                                      │     miss? yt-dlp --download-sections "*0-30"
                                                      │           → write preview.mp3
                                                      └── http.ServeFile (Range support)
Browser ◄──── audio/mpeg (30s clip, ORIGINAL key, WITH vocals) ────
```

`<audio>` plays the 30s clip in the original key. Missing or tampered sig → 400 Bad Request, no yt-dlp call.

---

## Stage 3 — Iterate on key (Preview-shift, ~1-2s per try)

User drags `KeySelector.vue` to -2 semitones. Frontend fires:

```
Browser ──POST /api/preview-shift {video_id, sig, semitones:-2}──► Go :8080
                                                                │
                                                                ├── validSig(videoId, sig)  ← 400 if mismatch
                                                                ├── validate semitones in [-5,+5]
                                                                ├── check tmp/cache/.../preview-shifts/-2.mp3
                                                                │     hit?  serve
                                                                │     miss:
                                                                │        ensure preview.mp3 exists
                                                                │        │
                                                                │        └── HTTP ──► Python :8090
                                                                │                       POST /shift
                                                                │                       { input: preview.mp3,
                                                                │                         semitones: -2,
                                                                │                         output: -2.mp3 }
                                                                │                       │
                                                                │                       ├── pyrubberband (preserves tempo)
                                                                │                       ├── librosa write WAV
                                                                │                       └── ffmpeg → 128kbps MP3
                                                                │                       (~1-2s for 30s clip)
                                                                └── serve -2.mp3
Browser ◄──── 30s clip, shifted -2 semitones, WITH vocals ────
```

User tries -3, then +1, then settles on -2. Each new semitone is ~1-2s cold; repeats are instant (cached per-semitone).

**Why this stage exists**: Demucs has not run yet. The user finds the right key cheaply, before paying any expensive cost.

---

## Stage 4 — Commit to the full song (Generate, 90-180s cold)

User clicks "Generate Full Song" with semitones=-2:

```
Browser ──POST /api/generate {video_id, sig, semitones:-2}──► Go :8080
                                                          │
                                                          ├── validSig(videoId, sig)  ← 400 if mismatch
                                                          ├── enqueue into worker pool
                                                          │   (MAX_CONCURRENT_JOBS=1)
                                                          └── return {job_id} immediately
Browser ◄──── {"job_id":"abc123"} ─────────────────────
```

Frontend opens an SSE stream:

```
Browser ──GET /api/status/abc123 (SSE)──► Go :8080
Browser ◄── event: queued        position:1
       ◄── event: downloading    (yt-dlp full song → original.wav)        ~10-30s
       ◄── event: separating     (Python /separate, Demucs → vocals.wav   60-120s
                                  + no_vocals.wav)
       ◄── event: melody         (Python /melody, CREPE on vocals.wav     30-60s
                                  → melody.json with original-key Hz)
       ◄── event: shifting       (Python /shift on no_vocals.wav, -2      5-15s
                                  → shifted/-2/audio.mp3)
       ◄── event: done
```

`ProcessingStatus.vue` displays each stage. Cache after this run:

```
tmp/cache/fJ9rUzIMcZQ/
  preview.mp3                    (from Stage 2)
  preview-shifts/-2.mp3          (from Stage 3)
  original.wav                   ← new
  vocals.wav                     ← new (Demucs)
  no_vocals.wav                  ← new (Demucs)
  melody.json                    ← new (CREPE on vocals)
  shifted/-2/audio.mp3           ← new
```

**Why CREPE runs on `vocals.wav`, not `original.wav`**: CREPE is monophonic. On a full mix it locks onto whichever pitch is loudest (often bass or lead guitar). Demucs first → CREPE on isolated voice → accurate melody.

---

## Stage 5 — Sing (instant after Stage 4)

Frontend swaps audio source and fetches melody:

```
Browser ──GET /api/audio/fJ9rUzIMcZQ/-2?sig=<HMAC>──► Go :8080
                                                       ├── validSig(videoId, sig)
                                                       └── http.ServeFile shifted/-2/audio.mp3
Browser ◄──── full instrumental, in key -2 ────

Browser ──GET /api/melody/fJ9rUzIMcZQ/-2?sig=<HMAC>──► Go :8080
                                                        ├── validSig(videoId, sig)
                                                        └── read melody.json
                                                            transpose each hz by 2^(-2/12)
                                                            (cheap math, no Python call)
Browser ◄──── { hop_ms, frames: [[t, hz, note, conf], ...] } ────
```

User clicks "Start Singing" → mic permission (one-time headphones tooltip) → AudioWorklet spins up:

```
Each frame (~every 30ms):
  1. mic buffer → pitchy.findPitch() → (detectedHz, clarity)
  2. target = melody.frames.find(closest to audio.currentTime)
                 ↑ NOT performance.now(), because <audio>
                   has ~100-200ms buffer latency
  3. centsOff = 1200 * log2(detectedHz / targetHz)
  4. color: ±25c green, ±50c yellow, >50c red
  5. PitchDiagram.vue: scroll SVG one tick — blue target line + colored user dot
     PitchMeter.vue: show "D4 +12c" etc.
```

---

## Cost on subsequent visits (within TTL window)

| Scenario | Cost | What runs |
|---|---|---|
| Same song, same key | instant | everything cached |
| Same song, different key | 5-15s | only `/shift` on cached `no_vocals.wav` + transcode |
| New song | 90-180s | full pipeline cold |
| **Same song, after TTL expiry** | 90-180s | full pipeline re-runs — accepted tradeoff |

The trick that makes this fast: **stems (`vocals.wav`, `no_vocals.wav`, `melody.json`) are cached per `video_id` alone**, not per key. Within the TTL window, Demucs and CREPE run exactly once per song. Pitch shift runs once per (song, key) pair.

## Cache lifetime — TTL, not permanent

Cache files are intentionally non-persistent:

- **TTL** configurable via `CACHE_TTL_HOURS` env var (default **24h**).
- **Phase 1 (local disk)**: a cleanup goroutine in `services/storage.go` runs every `CACHE_CLEANUP_INTERVAL_MIN` (default 10 min), deletes files with mtime older than TTL, prunes empty `{video_id}/` directories.
- **Phase 2 (cloud)**: bucket lifecycle policy (S3 / R2 / GCS — all support 1-day minimum granularity).
- **Why**: most songs are sung a few times then never revisited. Indefinite storage of cold songs is pure cloud-cost waste.
- **Tradeoff**: a user who returns after TTL expiry re-pays 90-180s. The UI doesn't differentiate — cache miss looks identical to a first-time generate.

All cache I/O goes through the `Storage` interface (`LocalPath / Has / Commit / Open`). `Has()` is TTL-aware: it returns false for files past TTL even before the cleanup goroutine has physically deleted them, preventing a race where a stale file is served moments before deletion.

Note: this is separate from the **JobStore 1h TTL**, which evicts in-memory job-status records (small structs, lost on restart — fine).

---

## The flow in one sentence

Search yields candidates → preview lets you hear the original → preview-shift lets you cheaply find your key → generate runs the heavy pipeline only once you've committed → audio + melody + mic come together for the sing-along.
