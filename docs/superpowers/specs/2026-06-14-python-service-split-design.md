# Python Service Split

**Date:** 2026-06-14
**Status:** Design locked. Implementation plan pending.
**Related:** `docs/superpowers/specs/2026-06-13-deployment-design.md` §4.5, `docs/superpowers/specs/2026-06-14-processor-url-handoff-design.md`, memory `project-deployment-plan`.

---

## 1. Goal

Split the monolithic `audio-processor/` Python service into two independent services that match the deployment topology: a small CPU service (search + shift + preview-key) destined for the Go-backend VM, and a heavy GPU service (separate + melody) destined for Hugging Face Spaces (ZeroGPU). Repoint Go's `YouTubeService` to the CPU service so `/search` is wired to the correct endpoint. No deployment, no Docker, no HF Space wiring — those are subsequent plans.

## 2. Non-goals

- Dockerfiles, fly.toml / EC2 user-data, HF Space `app.py`, `@spaces.GPU` decorator. Deferred to the deployment-packaging plan.
- yt-dlp PoT provider packaging (separate concern).
- Anti-abuse stack (sig payload change, Turnstile, WAF, CORS lock-down).
- Frontend deploy prep.
- Removing the legacy `PYTHON_PROCESSOR_URL` env var — it stays as the default-fallback for `CPU_PROCESSOR_URL` / `GPU_PROCESSOR_URL` so existing dev environments don't break. Removed once both URLs are universally set.

## 3. Target repo layout

```
cantus/
├── backend/                       # Go (unchanged except one line in main.go)
├── frontend/                      # Vue (unchanged)
├── audio-processor-cpu/           # NEW
│   ├── main.py
│   ├── requirements.txt           # fastapi, httpx, ytmusicapi, pyrubberband, librosa, soundfile, …
│   ├── logging_config.py          # copy of shared file
│   ├── routers/
│   │   ├── __init__.py
│   │   ├── _io_url.py             # copy of shared file
│   │   ├── search.py
│   │   ├── shift.py
│   │   └── preview_key.py
│   ├── services/
│   │   ├── pitch_service.py
│   │   ├── preview_key_service.py
│   │   └── ytmusic_service.py
│   ├── tests/
│   │   ├── test_io_url.py
│   │   ├── test_pitch_service.py
│   │   ├── test_preview_key_router.py
│   │   ├── test_preview_key_service.py
│   │   ├── test_search_router.py
│   │   ├── test_shift_router.py
│   │   └── test_ytmusic_service.py
│   ├── pyproject.toml
│   └── .env.example
├── audio-processor-gpu/           # RENAMED from audio-processor/
│   ├── main.py
│   ├── requirements.txt           # fastapi, httpx, torch, demucs, crepe, tensorflow, …
│   ├── logging_config.py          # copy of shared file
│   ├── routers/
│   │   ├── __init__.py
│   │   ├── _io_url.py             # copy of shared file
│   │   ├── separate.py
│   │   └── melody.py
│   ├── services/
│   │   ├── demucs_service.py
│   │   └── melody_service.py
│   ├── tests/
│   │   ├── test_demucs_service.py
│   │   ├── test_health.py
│   │   ├── test_io_url.py
│   │   ├── test_logging_config.py
│   │   ├── test_melody_router.py
│   │   ├── test_melody_service.py
│   │   └── test_separate_router.py
│   ├── pyproject.toml
│   └── .env.example
└── audio-processor/               # DELETED at end of plan
```

The CPU service ships only the CPU-bound deps. Image size shrinks from ~3 GB (torch + demucs + tensorflow + crepe) to ~400 MB.

## 4. Locked design decisions

### 4.1 Sibling dirs at repo root

`audio-processor-cpu/` and `audio-processor-gpu/` sit beside `backend/` and `frontend/`. Each is a self-contained Python project with its own `requirements.txt`, `tests/`, virtualenv, and `pyproject.toml`. One dir = one deployable.

**Why:** Mirrors deployment topology one-to-one. Dockerfiles per service have clean build contexts (no parent-dir gymnastics). New contributors see immediately that there are two Python services.

### 4.2 Duplicate shared files into each service

`_io_url.py` (httpx download/upload helpers from Plan #2) and `logging_config.py` (5-line setup) are copied verbatim into both `audio-processor-cpu/routers/_io_url.py` + `audio-processor-cpu/logging_config.py` and the same paths under `audio-processor-gpu/`.

**Why:** Both files are tiny, the contracts are stable (httpx wrappers + log setup), and YAGNI says we don't extract a shared package for ~80 LOC. Each service stays self-contained and pip-installable. Drift risk is real but low; cost of two-file edits is acceptable for a long time.

**Convention:** Add a sentence to CLAUDE.md noting these two files are shared verbatim. No tooling enforcement yet (defer until drift actually happens).

### 4.3 Repoint `YouTubeService` to `cfg.CPUProcessorURL`

`backend/cmd/server/main.go` swaps `cfg.PythonProcessorURL` → `cfg.CPUProcessorURL` when constructing `PythonYouTubeService`. `YouTubeService` itself remains a distinct interface — it owns yt-dlp orchestration (`DownloadFull`, `DownloadPreview`) which is Go-local work, not Python work. The single Python call (`Search` → `/search`) just goes to the CPU service URL.

**Why:** Minimal Go churn. We defer the spec §6 idea of folding `Search` into `CPUProcessorClient` (more invasive rename + interface churn) to a later cleanup if it ever pays off. `YouTubeService` keeps its identity for now.

### 4.4 Local dev: two uvicorn processes on different ports

- `audio-processor-gpu` stays on **:8090** (matches the current `PYTHON_PROCESSOR_URL` default — no env churn for existing devs).
- `audio-processor-cpu` runs on **:8091** (new; `CPU_PROCESSOR_URL=http://localhost:8091`).
- A `dev.sh` script (and/or `Procfile` for `overmind`/`honcho`) at repo root spawns Go (:8080), CPU service (:8091), GPU service (:8090), and frontend (:5173) in one command. Documented in README. Devs who want granular control still get four-terminal workflow.

**Why:** Mirrors production exactly (two separate services). Avoids the `app_dev.py` trap where dev-only mounting masks a real deploy split.

### 4.5 Separate venvs per service

Each Python project owns its own `.venv/`. No shared venv. No editable installs across services. `pip install -r requirements.txt` from inside each dir.

**Why:** Independent dependency graphs. CPU service's deps don't pollute the GPU service's environment and vice versa. Mirrors the eventual two-Docker-image reality.

### 4.6 `PYTHON_PROCESSOR_URL` stays as fallback

The Go config keeps `PYTHON_PROCESSOR_URL` as the default for both `CPU_PROCESSOR_URL` and `GPU_PROCESSOR_URL` (existing behavior from Plan #2). Devs still on `PYTHON_PROCESSOR_URL=http://localhost:8090` get both routed to the GPU service — which means `/search` and `/shift` and `/preview-key` will 404, but explicit overrides are documented. Removal of `PYTHON_PROCESSOR_URL` is deferred until everyone is on the new vars.

**Why:** Keeps this plan tight; removal is a five-line follow-up once the migration window closes.

## 5. Migration approach

Atomic single-PR series (consistent with Plans #1/#2). Five logical steps, each TDD'd and committed:

1. **Scaffold `audio-processor-cpu/`** with `main.py`, `requirements.txt` (minimal deps), empty router dir, empty services dir, empty tests dir, `pyproject.toml`, `.env.example`. Verify `uvicorn main:app --port 8091` boots and `/health` returns 200.
2. **Move `/search`** — `routers/search.py`, `services/ytmusic_service.py`, the two test files, into CPU service. Add `_io_url.py` + `logging_config.py` copies. Update CPU's `main.py` to mount the router. Add `ytmusicapi` + httpx to CPU `requirements.txt`. Verify CPU tests pass.
3. **Move `/shift`** — `routers/shift.py`, `services/pitch_service.py`, test files. Add `pyrubberband`, `soundfile` deps to CPU. Verify.
4. **Move `/preview-key`** — `routers/preview_key.py`, `services/preview_key_service.py`, test files. Add `librosa` dep to CPU. Verify.
5. **Rename `audio-processor/` → `audio-processor-gpu/`** in a single `git mv` step. Trim `audio-processor-gpu/requirements.txt` to GPU-only deps (drop `ytmusicapi`, `pyrubberband`, `librosa` if not also needed by Demucs/CREPE). Update GPU's `main.py` to mount only `separate` + `melody`. Delete the moved routers/services from GPU. Verify GPU tests pass.
6. **Repoint Go**: `backend/cmd/server/main.go` changes one line (`cfg.PythonProcessorURL` → `cfg.CPUProcessorURL` in `NewPythonYouTubeService`). Run Go tests.
7. **Add `dev.sh`** to repo root. Update README + CLAUDE.md with the new local-dev story.

At each step, both services boot and the moved endpoint works end-to-end through Go (manual curl smoke per moved endpoint).

## 6. Configuration matrix

No new env vars. Existing vars (from Plan #2) are reused:

| Var | Current default | Post-split meaning |
|---|---|---|
| `PYTHON_PROCESSOR_URL` | `http://localhost:8090` | Legacy fallback for both URLs below. Removal deferred. |
| `CPU_PROCESSOR_URL` | (fallback) | Now points at `http://localhost:8091` for split local dev. |
| `GPU_PROCESSOR_URL` | (fallback) | Stays `http://localhost:8090`. |
| `CPU_PROCESSOR_TIMEOUT_SECONDS` | 30 | Unchanged. |
| `GPU_PROCESSOR_TIMEOUT_SECONDS` | 180 | Unchanged. |

CPU service's `.env.example` documents `DEVICE` (still relevant for some librosa fallbacks); GPU service's `.env.example` documents `DEVICE` (`cpu` / `mps` / `cuda`) and any Demucs/CREPE knobs.

## 7. Testing strategy

- Each service has its own `tests/` dir + own pytest run.
- Tests do NOT cross service boundaries. No new integration tests at this layer.
- The existing tests (currently in `audio-processor/tests/`) physically move with the routers/services they cover. No rewrites — they target Pydantic schemas + service-level stubs, both of which are unchanged by the split.
- `_io_url.py` and `logging_config.py` get their tests copied alongside them: each service runs the full battery against its local copy. This catches drift between the two `_io_url.py` files indirectly (if one diverges and breaks an assumption, its test fails).
- `dev.sh` smoke: after each move step, exercise the relevant curl flow through Go (e.g. `/api/songs/search` after step 2, `/api/preview-shift` after step 3).

## 8. Deployment topology change (informational, not in scope)

The deployment-packaging plan will run on **AWS EC2 free tier (t3.micro)**, not Fly.io — Fly ended their free tier. EC2 free tier offers 1 vCPU + **1 GB RAM**, which is tight for Go backend + Python CPU service + bgutil PoT sidecar on one VM. Fallbacks:

- **Oracle Always-Free** — 4 ARM cores + 24 GB RAM, no expiration. Bigger learning curve.
- **t3.small** — ~$15/mo after the 12-month EC2 free window.

The Python-split design is provider-agnostic; this note is here to keep memory consistent. The packaging plan will pick.

## 9. Risks and mitigations

| Risk | Mitigation |
|---|---|
| `_io_url.py` drift between services | CLAUDE.md convention + tests run against both copies. Tooling enforcement deferred until drift actually bites. |
| Devs accidentally hit `/search` on the GPU service after split (404) | New `.env.example` files document the split URLs; README's "running locally" section spells out which service serves which endpoint. |
| Two-uvicorn dev workflow adds friction | `dev.sh` / `Procfile` makes it one command. Devs who want granular control still use four terminals. |
| Renaming `audio-processor/` breaks open editor tabs / external scripts / CI configs | Single atomic `git mv` step in the migration; touch CI/script references in the same commit. Grep for `audio-processor[^/-]` across the repo before merging. |
| GPU `requirements.txt` accidentally keeps now-unused CPU deps (e.g. `ytmusicapi`) | Plan step 5 explicitly trims unused deps. Verify by running `pip-compile --strip-extras` or manual review after the move. |
| Existing Demucs/CREPE model caches under `~/.cache/torch/`, etc. survive but are no longer co-located with their service | Cache lives in user home, not in repo — survives the rename automatically. |

## 10. Out of scope (explicitly deferred)

- Dockerfiles, EC2 user-data scripts, HF Space `app.py`, `@spaces.GPU(duration=120)` decorator, pre-baked Demucs weights in image.
- Removal of `PYTHON_PROCESSOR_URL` env var.
- Folding `Search` from `YouTubeService` into `CPUProcessorClient` (spec §6 idea; would change Go-side interface boundaries).
- Anti-abuse stack (sig payload change, Turnstile, WAF, CORS lock-down).
- Frontend deployment prep (Pages env vars, session_id, SSE reconnect).
- yt-dlp PoT provider packaging.
- Extracting `_io_url.py` / `logging_config.py` into a shared package (revisit if drift becomes painful or a third service appears).
