# Modal + BS-Roformer GPU service — design

**Status:** approved 2026-06-15
**Supersedes (for GPU service only):** `2026-06-13-deployment-design.md` §"Python GPU (HF Space)"
**Related:** [[project-modal-gpu-plan]], [[project-deployment-plan]]

## Goal

Rewrite the existing `audio-processor-gpu/` service so that it runs on Modal.com (A10G, scale-to-zero), uses **BS-Roformer** (`model_bs_roformer_ep_368_sdr_12.9628.ckpt`) for vocal separation instead of Demucs, and keeps the Go backend's `PythonProcessorClient` contract unchanged. CREPE-based melody extraction stays on the same container so a warm GPU serves both pipeline stages.

## Non-goals

- Any change to the Go backend's `ProcessorClient` interface, request/response shapes, or HMAC sig flow.
- Any change to R2 storage, presigned URL semantics, or cache layout.
- Switching the melody model away from CREPE.
- Cloudflare Pages / EC2 / Turnstile work (tracked separately in the deployment plan).

## Constraints

1. Stay within the Modal $30/mo Starter credit. Means: `min_containers=0`, `scaledown_window=30`, `timeout=120`, `enable_memory_snapshot=True`.
2. Go-side contract is frozen — `Separate(ctx, inputURL, vocalsOutputURL, noVocalsOutputURL)` and `Melody(ctx, vocalsInputURL, outputURL)` must keep their current JSON bodies and 204 responses.
3. Local development on `uvicorn :8090` continues to work (CPU/MPS) so Modal credit isn't burned on iteration.
4. The Roformer checkpoint (~640 MB) lives on a Modal **Volume**, not baked into the image, and is provisioned by a one-shot seed command rather than lazy-downloaded.

## Architecture

```
backend (EC2 later, localhost today)
  PythonProcessorClient ─ HTTPS ─► Modal ASGI endpoint  (or local uvicorn :8090)
                                     │  FastAPI app (unchanged shape)
                                     │
                                     ├── POST /separate ─► AudioProcessor (Modal class on A10G)
                                     │                      ├── @enter snap=True : load Roformer ckpt from /models
                                     │                      ├── @enter snap=False: move model to cuda
                                     │                      └── separate → upload vocals.wav + no_vocals.wav
                                     │
                                     └── POST /melody   ─► AudioProcessor.melody
                                                            └── CREPE on vocals → upload melody.json
```

One Modal `App`, one `Modal.cls` (`AudioProcessor`) exposing two methods that the FastAPI app delegates to. The class is mounted via `@modal.asgi_app()` so the public surface is a single ASGI URL.

## File layout (inside existing `audio-processor-gpu/`)

```
audio-processor-gpu/
  main.py                       # local FastAPI app (uvicorn) — keeps DEVICE=cpu|mps path
  modal_app.py                  # NEW. Modal App + AudioProcessor cls + asgi_app
  seed_models.py                # NEW. `modal run seed_models.py` — populates the Volume
  routers/
    separate.py                 # rewritten: delegates to RoformerService
    melody.py                   # unchanged behavior, deps re-pointed
    _io_url.py                  # unchanged (download/upload helpers)
  services/
    roformer_service.py         # NEW. wraps audio-separator's Separator for Roformer
    melody_service.py           # unchanged (CREPE)
    demucs_service.py           # DELETED
  requirements.txt              # rewritten — drop demucs/dora/julius/openunmix/lameenc;
                                #              add audio-separator, modal, onnxruntime-gpu
  .env.example                  # add MODEL_PATH, ROFORMER_MODEL_FILENAME
  tests/                        # rewritten where they referenced Demucs
```

## Component design

### `services/roformer_service.py`

```python
class RoformerService:
    """In-process BS-Roformer separation; loads model once per process."""

    def __init__(self, model_dir: str, model_filename: str, device: str) -> None: ...
    def separate(self, input_path: str, output_dir: str) -> None:
        # produces vocals.wav + no_vocals.wav (same filenames as DemucsService)
        # idempotent on existing non-empty outputs (same behavior as DemucsService)
```

- Wraps `audio_separator.separator.Separator`.
- `model_dir` = `/models` on Modal, `./tmp/models` locally.
- Output filenames stay `vocals.wav` / `no_vocals.wav` so `routers/separate.py` and Go's contract don't move.
- `audio-separator` writes its own filenames by default; the service renames them after run to match the contract.
- Idempotency check identical to the previous `DemucsService`: skip if both targets exist + non-empty.

### `routers/separate.py`

- Request body unchanged: `{input_url, vocals_output_url, no_vocals_output_url}`.
- Status code unchanged: 204.
- Only change: `get_demucs_service()` → `get_roformer_service()` (returns `RoformerService` via `@lru_cache(maxsize=1)`).
- `_io_url.download_to_temp` / `upload_from_path` reused as-is.

### `routers/melody.py`

- No behavioral change. Verify it still imports cleanly after the dependency churn.

### `modal_app.py`

```python
import modal

image = modal.Image.debian_slim(python_version="3.12") \
    .apt_install("ffmpeg") \
    .pip_install_from_requirements("requirements.txt")

volume = modal.Volume.from_name("cantus-models", create_if_missing=True)
app = modal.App("cantus-audio-processor")

@app.cls(
    gpu="A10G",
    image=image,
    volumes={"/models": volume},
    min_containers=0,
    scaledown_window=30,
    enable_memory_snapshot=True,
    timeout=120,
)
class AudioProcessor:
    @modal.enter(snap=True)
    def load(self):
        # constructs RoformerService → loads ckpt from /models into RAM
        ...

    @modal.enter(snap=False)
    def to_gpu(self):
        # moves model to cuda (post-snapshot)
        ...

    @modal.asgi_app()
    def fastapi_app(self):
        # mounts the same FastAPI app from main.py, with the singleton
        # RoformerService swapped to the instance loaded above
        ...
```

- The `@modal.asgi_app` returns the existing FastAPI app, with `get_roformer_service` and `get_melody_service` dependency-overridden to point at the warm singletons on the class.
- CREPE service is also loaded in `@modal.enter(snap=True)` so its TensorFlow weights are in the snapshot.

### `seed_models.py`

```python
# `modal run seed_models.py` — idempotent
@app.function(volumes={"/models": volume}, timeout=600)
def seed():
    # if /models/model_bs_roformer_ep_368_sdr_12.9628.ckpt missing → download
    # CREPE weights handled by the crepe pip package on first call; nothing to seed
    volume.commit()
```

- Source URL for the ckpt: pinned in code (HuggingFace mirror or the official release URL — to be confirmed during implementation; spec leaves this open because the exact hosting URL drifts).
- Verifies SHA-256 after download.

### `main.py` (local uvicorn path)

- Stays as today's small FastAPI app; mounts `separate` + `melody` routers.
- `DEVICE` env still drives `cpu` / `mps` / `cuda` for local iteration.
- The Modal entrypoint reuses this app object via `@modal.asgi_app`, but overrides the service singletons so it doesn't try to construct a CPU-only `RoformerService` on import.

## Data flow (no change from today)

1. Go signs R2 input URL (GET) + two output URLs (PUT), POSTs `/separate`.
2. FastAPI handler: `download_to_temp(input_url)` → `RoformerService.separate` → `upload_from_path(vocals)` + `upload_from_path(no_vocals)` → 204.
3. Go later POSTs `/melody` with the vocals R2 GET URL + melody.json PUT URL. Same pattern, CREPE this time.

## Error handling

- All non-2xx upstream (R2 download/upload) → propagate as 500 with `detail` (existing behavior).
- Roformer load failure (missing ckpt in `/models`) → 500 with `detail="model not seeded; run seed_models.py"`. This makes the deploy ordering failure mode obvious.
- Modal-side `timeout=120` is the worst-case bill guard. Go's `PROCESSOR_TIMEOUT_SECONDS=180` stays as the outer bound and absorbs the ~5–10s cold start.

## Testing

- `tests/test_roformer_service.py` — table-driven; idempotency, missing input, output filename rename. Real ckpt not required for the unit test (mock the inner `Separator`).
- `tests/test_separate_route.py` — happy-path with a mocked `RoformerService`, ensures request/response contract is preserved.
- `tests/test_melody_route.py` — unchanged.
- Manual integration: `modal serve modal_app.py` + a curl with a small WAV URL → verify both stems uploaded.

## Migration steps (sketch — full sequencing belongs to the plan)

1. New deps + `roformer_service.py` + tests, behind a feature toggle locally (still works on uvicorn with DEVICE=cpu/mps).
2. Delete `demucs_service.py` once parity is verified locally.
3. Add `modal_app.py` + `seed_models.py`; deploy seed; benchmark cold + warm against a 4-min sample.
4. Flip Go's `PROCESSOR_URL` to the Modal endpoint in a staged config (still defaults to localhost for dev).
5. Update `.env.example`, CLAUDE.md (Python service section), and `project_deployment_plan.md` references that still say "HF Space (ZeroGPU)".

## Open items deferred to implementation

- Exact Roformer checkpoint download URL + SHA-256 (verify at code-time, don't lock now).
- Whether `onnxruntime-gpu` vs `torch` is the right inference backend in `audio-separator` for A10G — `audio-separator` supports both; pick whichever benchmarks faster in step 3.
- Whether `model_bs_roformer_ep_368_sdr_12.9628.ckpt` materially differs in speed from `ep_317`. The plan asks for both to be benchmarked; this spec locks ep_368 as the default per user preference.
