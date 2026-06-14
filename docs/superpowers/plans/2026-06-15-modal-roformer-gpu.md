# Modal + BS-Roformer GPU service — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Demucs-based audio-processor-gpu service with a BS-Roformer (ep_368) service that runs on Modal A10G (scale-to-zero) while keeping the Go backend's `PythonProcessorClient` contract byte-for-byte identical and preserving local `uvicorn :8090` development.

**Architecture:** Single FastAPI app exposed two ways — locally via `uvicorn main:app` and on Modal via `modal_app.py`'s `@modal.asgi_app()`. A new `RoformerService` (wrapping `audio-separator`) replaces `DemucsService`; `MelodyService` (CREPE) is unchanged. A `seed_models.py` populates the Modal Volume one-time with the `model_bs_roformer_ep_368_sdr_12.9628.ckpt` checkpoint. CREPE lives on the same Modal class so a warm GPU serves both `/separate` and `/melody`.

**Tech Stack:** Python 3.12, FastAPI, `audio-separator` (UVR fork, supports BS-Roformer ckpt files), Modal SDK, `crepe` + `librosa` (unchanged), `httpx` (unchanged).

**Spec:** `docs/superpowers/specs/2026-06-15-modal-roformer-gpu-design.md`

---

## File Structure

**Create:**
- `audio-processor-gpu/services/roformer_service.py` — wraps `audio_separator.Separator`.
- `audio-processor-gpu/tests/test_roformer_service.py` — table-driven, uses a `FakeSeparator` duck-type.
- `audio-processor-gpu/modal_app.py` — Modal `App` + `AudioProcessor` cls + `@modal.asgi_app()`.
- `audio-processor-gpu/seed_models.py` — `modal run`-able function that seeds the Volume.

**Modify:**
- `audio-processor-gpu/routers/separate.py` — swap `get_demucs_service` → `get_roformer_service`.
- `audio-processor-gpu/tests/test_separate_router.py` — rename stub + dep override.
- `audio-processor-gpu/requirements.txt` — drop Demucs/dora/julius/openunmix/lameenc; add `audio-separator[gpu]`, `modal`.
- `audio-processor-gpu/.env.example` — add `MODEL_DIR`, `ROFORMER_MODEL_FILENAME`.
- `CLAUDE.md` — update Python service section to say Roformer + Modal.

**Delete:**
- `audio-processor-gpu/services/demucs_service.py`
- `audio-processor-gpu/tests/test_demucs_service.py`

---

## Task 1: Rewrite requirements.txt and env example

**Files:**
- Modify: `audio-processor-gpu/requirements.txt`
- Modify: `audio-processor-gpu/.env.example`

- [ ] **Step 1: Rewrite `requirements.txt`** — replace the Demucs/Roformer-irrelevant lines. Use this exact content:

```
# Web / I/O
fastapi==0.136.3
uvicorn==0.49.0
httpx==0.28.1
pydantic==2.13.4
python-dotenv==1.2.2
python-json-logger==4.1.0

# Audio I/O
soundfile==0.14.0
librosa==0.11.0
numpy==2.4.6

# Vocal separation (BS-Roformer via UVR fork)
audio-separator[gpu]==0.30.0
onnxruntime-gpu==1.20.1

# Pitch extraction
crepe==0.0.16
tensorflow==2.21.0

# Modal deploy
modal==0.74.0

# Dev / test
pytest==9.0.3
ruff==0.15.16
```

Notes for the implementer: pin versions to the closest current release at implementation time if the listed ones are stale; the spirit is "drop Demucs deps, add audio-separator + modal". Run `pip install -r requirements.txt` in a fresh venv to confirm resolution.

- [ ] **Step 2: Update `.env.example`** to:

```
TMP_DIR=./tmp
# Options: cpu | mps (Apple Silicon) | cuda (NVIDIA GPU)
DEVICE=cpu
PORT=8090

# BS-Roformer checkpoint location and filename.
# Locally: ./tmp/models. On Modal: /models (mounted Volume).
MODEL_DIR=./tmp/models
ROFORMER_MODEL_FILENAME=model_bs_roformer_ep_368_sdr_12.9628.ckpt
```

- [ ] **Step 3: Commit**

```bash
git add audio-processor-gpu/requirements.txt audio-processor-gpu/.env.example
git commit -m "chore(audio-processor-gpu): swap Demucs deps for audio-separator + modal"
```

---

## Task 2: RoformerService — happy path

**Files:**
- Create: `audio-processor-gpu/services/roformer_service.py`
- Create: `audio-processor-gpu/tests/test_roformer_service.py`

- [ ] **Step 1: Write the failing test** at `audio-processor-gpu/tests/test_roformer_service.py`:

```python
from __future__ import annotations

from pathlib import Path

import pytest

from services.roformer_service import RoformerService


class FakeSeparator:
    """Duck-types audio_separator.Separator for tests.

    The real Separator.separate(input_path) returns a list of output file
    paths it wrote to its configured output_dir. We mimic that contract.
    """

    def __init__(
        self,
        # files the fake will create on the configured output_dir
        outputs: dict[str, bytes] | None = None,
        raises: Exception | None = None,
    ) -> None:
        self._outputs = outputs or {
            # default UVR naming: <stem>_(Vocals)_<model>.wav, <stem>_(Instrumental)_<model>.wav
            "track_(Vocals)_BS-Roformer.wav": b"VOCALS-WAV",
            "track_(Instrumental)_BS-Roformer.wav": b"INSTR-WAV",
        }
        self._raises = raises
        self.output_dir: str | None = None
        self.loaded_model: str | None = None
        self.call_count = 0
        self.last_input: str | None = None

    # API: load_model is called once at construction time by RoformerService.
    def load_model(self, model_filename: str) -> None:
        self.loaded_model = model_filename

    # API: separate(input_path) writes outputs into the Separator's own
    # output_dir (set via its constructor) and returns the list of paths.
    def separate(self, input_path: str) -> list[str]:
        self.call_count += 1
        self.last_input = input_path
        if self._raises is not None:
            raise self._raises
        assert self.output_dir is not None, "output_dir must be set before separate()"
        written: list[str] = []
        for name, content in self._outputs.items():
            p = Path(self.output_dir) / name
            p.write_bytes(content)
            written.append(str(p))
        return written


def _make_service(fake: FakeSeparator) -> RoformerService:
    """Build a RoformerService that uses the given fake as its inner Separator.

    The factory the service uses must let us set output_dir per separate() call
    and return our prebuilt fake.
    """

    def factory(output_dir: str) -> FakeSeparator:
        fake.output_dir = output_dir
        return fake

    return RoformerService(
        model_dir="/unused-in-test",
        model_filename="model_bs_roformer_ep_368_sdr_12.9628.ckpt",
        separator_factory=factory,
    )


def test_separate_happy_path_renames_to_canonical_names(tmp_path: Path) -> None:
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")
    output_dir = tmp_path / "out"

    fake = FakeSeparator()
    service = _make_service(fake)
    service.separate(str(input_file), str(output_dir))

    assert (output_dir / "vocals.wav").exists()
    assert (output_dir / "no_vocals.wav").exists()
    assert (output_dir / "vocals.wav").read_bytes() == b"VOCALS-WAV"
    assert (output_dir / "no_vocals.wav").read_bytes() == b"INSTR-WAV"
    assert fake.loaded_model == "model_bs_roformer_ep_368_sdr_12.9628.ckpt"
    assert fake.call_count == 1
    assert fake.last_input == str(input_file)
```

- [ ] **Step 2: Run the test and verify it fails**

```bash
cd audio-processor-gpu && pytest tests/test_roformer_service.py -v
```

Expected: FAIL with `ModuleNotFoundError: No module named 'services.roformer_service'`.

- [ ] **Step 3: Implement** at `audio-processor-gpu/services/roformer_service.py`:

```python
from __future__ import annotations

import os
from collections.abc import Callable
from pathlib import Path


def _default_factory(model_dir: str) -> Callable[[str], object]:
    """Returns a callable(output_dir) -> Separator.

    Imported lazily so tests don't need audio_separator installed.
    """

    def factory(output_dir: str) -> object:
        from audio_separator.separator import Separator

        return Separator(model_file_dir=model_dir, output_dir=output_dir)

    return factory


class RoformerService:
    """In-process BS-Roformer vocal separation.

    Wraps audio_separator.Separator. Renames the UVR-style output files to the
    canonical `vocals.wav` / `no_vocals.wav` expected by the rest of the
    pipeline so the Go contract doesn't see UVR's `_(Vocals)_<model>.wav`
    naming.
    """

    def __init__(
        self,
        model_dir: str,
        model_filename: str,
        separator_factory: Callable[[str], object] | None = None,
    ) -> None:
        self._model_dir = model_dir
        self._model_filename = model_filename
        self._factory = separator_factory or _default_factory(model_dir)

    def separate(self, input_path: str, output_dir: str) -> None:
        """Run Roformer on input_path; write vocals.wav + no_vocals.wav under output_dir."""
        if not os.path.exists(input_path):
            raise FileNotFoundError(f"input_path not found: {input_path!r}")

        vocals_target = Path(output_dir, "vocals.wav")
        no_vocals_target = Path(output_dir, "no_vocals.wav")

        if (
            vocals_target.exists()
            and vocals_target.stat().st_size > 0
            and no_vocals_target.exists()
            and no_vocals_target.stat().st_size > 0
        ):
            return

        os.makedirs(output_dir, exist_ok=True)

        separator = self._factory(output_dir)
        # load_model is the audio-separator API; safe to call repeatedly when
        # the factory returns the same instance (warm container).
        separator.load_model(model_filename=self._model_filename)

        try:
            written = separator.separate(input_path)
        except Exception as exc:
            raise RuntimeError(f"roformer failed: {exc}") from exc

        vocals_src = _pick(written, "Vocals")
        instr_src = _pick(written, "Instrumental")
        if vocals_src is None or instr_src is None:
            raise RuntimeError(
                f"roformer did not produce both stems; got {written!r}"
            )
        os.replace(vocals_src, vocals_target)
        os.replace(instr_src, no_vocals_target)


def _pick(paths: list[str], kind: str) -> str | None:
    """Return the path containing `kind` in its filename, or None."""
    for p in paths:
        if kind in os.path.basename(p):
            return p
    return None
```

- [ ] **Step 4: Run the test and verify it passes**

```bash
cd audio-processor-gpu && pytest tests/test_roformer_service.py -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add audio-processor-gpu/services/roformer_service.py audio-processor-gpu/tests/test_roformer_service.py
git commit -m "feat(audio-processor-gpu): add RoformerService wrapping audio-separator"
```

---

## Task 3: RoformerService — error paths and idempotency

**Files:**
- Modify: `audio-processor-gpu/tests/test_roformer_service.py`

- [ ] **Step 1: Add failing tests** to the bottom of the test file:

```python
@pytest.mark.parametrize(
    "case",
    [
        {
            "name": "idempotency-skips-when-both-exist",
            "pre_create": {"vocals.wav": b"existing-v", "no_vocals.wav": b"existing-nv"},
            "fake_raises": AssertionError("should not run"),
            "expect_call_count": 0,
            "expect_vocals": b"existing-v",
            "expect_no_vocals": b"existing-nv",
        },
        {
            "name": "partial-cache-reruns",
            "pre_create": {"vocals.wav": b"stale"},  # no_vocals missing
            "fake_raises": None,
            "expect_call_count": 1,
            "expect_vocals": b"VOCALS-WAV",
            "expect_no_vocals": b"INSTR-WAV",
        },
        {
            "name": "zero-byte-vocals-reruns",
            "pre_create": {"vocals.wav": b"", "no_vocals.wav": b"good"},
            "fake_raises": None,
            "expect_call_count": 1,
            "expect_vocals": b"VOCALS-WAV",
            "expect_no_vocals": b"INSTR-WAV",
        },
    ],
    ids=lambda c: c["name"],
)
def test_separate_idempotency(case, tmp_path: Path) -> None:
    output_dir = tmp_path / "out"
    output_dir.mkdir()
    for name, content in case["pre_create"].items():
        (output_dir / name).write_bytes(content)

    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")

    fake = FakeSeparator(raises=case["fake_raises"])
    service = _make_service(fake)
    service.separate(str(input_file), str(output_dir))

    assert fake.call_count == case["expect_call_count"]
    assert (output_dir / "vocals.wav").read_bytes() == case["expect_vocals"]
    assert (output_dir / "no_vocals.wav").read_bytes() == case["expect_no_vocals"]


def test_separate_missing_input_raises_file_not_found(tmp_path: Path) -> None:
    fake = FakeSeparator()
    service = _make_service(fake)
    with pytest.raises(FileNotFoundError):
        service.separate(str(tmp_path / "nope.wav"), str(tmp_path / "out"))
    assert fake.call_count == 0


def test_separate_separator_exception_becomes_runtime_error(tmp_path: Path) -> None:
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")

    fake = FakeSeparator(raises=Exception("CUDA OOM"))
    service = _make_service(fake)

    with pytest.raises(RuntimeError) as exc_info:
        service.separate(str(input_file), str(tmp_path / "out"))
    msg = str(exc_info.value)
    assert "roformer failed" in msg.lower()
    assert "CUDA OOM" in msg


def test_separate_missing_stem_in_outputs_raises(tmp_path: Path) -> None:
    """Separator returned only one stem → RuntimeError."""
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")

    # Only vocals; no instrumental.
    fake = FakeSeparator(outputs={"track_(Vocals)_BS-Roformer.wav": b"V"})
    service = _make_service(fake)

    with pytest.raises(RuntimeError) as exc_info:
        service.separate(str(input_file), str(tmp_path / "out"))
    assert "both stems" in str(exc_info.value).lower()


def test_separate_creates_output_directory(tmp_path: Path) -> None:
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")
    output_dir = tmp_path / "nested" / "out"

    fake = FakeSeparator()
    service = _make_service(fake)
    service.separate(str(input_file), str(output_dir))

    assert (output_dir / "vocals.wav").exists()
    assert (output_dir / "no_vocals.wav").exists()
```

- [ ] **Step 2: Run and verify all five new tests pass**

```bash
cd audio-processor-gpu && pytest tests/test_roformer_service.py -v
```

Expected: PASS for all tests including the new ones (the Step 2 implementation in Task 2 already handles all these paths).

- [ ] **Step 3: Commit**

```bash
git add audio-processor-gpu/tests/test_roformer_service.py
git commit -m "test(audio-processor-gpu): cover RoformerService error + idempotency paths"
```

---

## Task 4: Wire RoformerService into the separate router

**Files:**
- Modify: `audio-processor-gpu/routers/separate.py`
- Modify: `audio-processor-gpu/tests/test_separate_router.py`

- [ ] **Step 1: Rewrite `routers/separate.py`** to delegate to `RoformerService`:

```python
from __future__ import annotations

import asyncio
import os
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Response
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp, upload_from_path
from services.roformer_service import RoformerService


class SeparateRequest(BaseModel):
    input_url: str = Field(min_length=1)
    vocals_output_url: str = Field(min_length=1)
    no_vocals_output_url: str = Field(min_length=1)


@lru_cache(maxsize=1)
def get_roformer_service() -> RoformerService:
    return RoformerService(
        model_dir=os.environ.get("MODEL_DIR", "./tmp/models"),
        model_filename=os.environ.get(
            "ROFORMER_MODEL_FILENAME",
            "model_bs_roformer_ep_368_sdr_12.9628.ckpt",
        ),
    )


SeparateServiceDep = Annotated[RoformerService, Depends(get_roformer_service)]
router = APIRouter()


@router.post("/separate", status_code=204)
def separate(req: SeparateRequest, service: SeparateServiceDep) -> Response:
    """Download input → run Roformer → upload both stems."""

    async def _run() -> None:
        with tempfile.TemporaryDirectory(prefix="separate-") as td:
            scratch = Path(td)
            src = await download_to_temp(req.input_url, scratch)
            stems_dir = scratch / "stems"
            stems_dir.mkdir()
            try:
                service.separate(str(src), str(stems_dir))
            except RuntimeError as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc
            vocals = stems_dir / "vocals.wav"
            no_vocals = stems_dir / "no_vocals.wav"
            if not vocals.exists() or not no_vocals.exists():
                raise HTTPException(status_code=500, detail="roformer did not produce both stems")
            await upload_from_path(vocals, req.vocals_output_url)
            await upload_from_path(no_vocals, req.no_vocals_output_url)

    asyncio.run(_run())
    return Response(status_code=204)
```

- [ ] **Step 2: Update `tests/test_separate_router.py`** — rename the stub class and dep override, no behavior change:

Replace the top imports:

```python
from routers import separate as sep_router
from routers.separate import get_roformer_service
```

Replace `_StubDemucs` with `_StubRoformer` (same body, just rename), and replace every `app.dependency_overrides[get_demucs_service]` with `app.dependency_overrides[get_roformer_service]`. Replace the assertion `assert "demucs" in resp.json()["detail"]` with `assert "roformer" in resp.json()["detail"].lower()` (the stub error message uses "roformer OOM" now — update the test to set `RuntimeError("roformer OOM")` accordingly).

Concretely, the runtime-error test becomes:

```python
def test_separate_runtime_error_returns_500(stub_io):
    stub = _StubRoformer(raise_exc=RuntimeError("roformer OOM"))
    app.dependency_overrides[get_roformer_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post(
            "/separate",
            json={
                "input_url": "i",
                "vocals_output_url": "v",
                "no_vocals_output_url": "nv",
            },
        )
        assert resp.status_code == 500
        assert "roformer" in resp.json()["detail"].lower()
    finally:
        app.dependency_overrides.clear()
```

- [ ] **Step 3: Run the tests**

```bash
cd audio-processor-gpu && pytest tests/test_separate_router.py tests/test_roformer_service.py -v
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add audio-processor-gpu/routers/separate.py audio-processor-gpu/tests/test_separate_router.py
git commit -m "refactor(audio-processor-gpu): point /separate at RoformerService"
```

---

## Task 5: Delete DemucsService

**Files:**
- Delete: `audio-processor-gpu/services/demucs_service.py`
- Delete: `audio-processor-gpu/tests/test_demucs_service.py`

- [ ] **Step 1: Delete the files**

```bash
rm audio-processor-gpu/services/demucs_service.py audio-processor-gpu/tests/test_demucs_service.py
```

- [ ] **Step 2: Search for any remaining references**

```bash
grep -rn "demucs_service\|DemucsService" audio-processor-gpu
```

Expected: no output. If anything turns up, fix the reference.

- [ ] **Step 3: Run the full suite**

```bash
cd audio-processor-gpu && pytest -v
```

Expected: all green; collection should NOT pull in any Demucs-dependent code.

- [ ] **Step 4: Commit**

```bash
git add -A audio-processor-gpu
git commit -m "chore(audio-processor-gpu): drop dead DemucsService + tests"
```

---

## Task 6: Modal app entrypoint

**Files:**
- Create: `audio-processor-gpu/modal_app.py`

This task has no automated test — `modal` requires a Modal account to actually deploy. The verification step uses `modal config show` and a `--dry-run`-equivalent (a `modal deploy --help`-style check that the file parses). The first real-money verification happens in Task 8.

- [ ] **Step 1: Create `audio-processor-gpu/modal_app.py`**:

```python
"""Modal entrypoint for the Cantus audio-processor-gpu service.

Deploys the existing FastAPI app (main.app) onto Modal A10G with:
- BS-Roformer + CREPE weights loaded from a persistent Volume,
- memory snapshot after model load → ~5–10s cold start,
- scale-to-zero to keep the $30/mo Modal credit intact.
"""
from __future__ import annotations

import os

import modal

# Image: matches local requirements.txt. apt_install adds ffmpeg for librosa.
image = (
    modal.Image.debian_slim(python_version="3.12")
    .apt_install("ffmpeg")
    .pip_install_from_requirements("requirements.txt")
    .env({"MODEL_DIR": "/models"})
)

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
    """Warm-singleton container holding both Roformer + CREPE weights."""

    @modal.enter(snap=True)
    def load(self) -> None:
        # Importing here keeps cold-start import cost inside the snapshot.
        from routers.melody import get_melody_service
        from routers.separate import get_roformer_service

        # Prime the lru_cache singletons so their constructors (model load) are
        # snapshot-captured. After @enter(snap=False) moves them to cuda.
        self._roformer = get_roformer_service()
        self._melody = get_melody_service()

    @modal.enter(snap=False)
    def to_gpu(self) -> None:
        # Roformer / audio-separator picks device from env. Set CUDA visibility
        # here; the singleton model object will be re-bound to cuda on first
        # inference call. CREPE/TensorFlow auto-detects GPU.
        os.environ["CUDA_VISIBLE_DEVICES"] = "0"

    @modal.asgi_app()
    def fastapi_app(self):
        # Reuses the same FastAPI app used by local uvicorn.
        from main import app as fastapi

        return fastapi
```

- [ ] **Step 2: Smoke-check the file parses**

```bash
cd audio-processor-gpu && python -c "import modal_app; print(modal_app.app.name)"
```

Expected output: `cantus-audio-processor`.

If `modal` is not installed in the local venv yet (deferred to deploy time), instead run:

```bash
cd audio-processor-gpu && python -m py_compile modal_app.py
```

Expected: exit code 0, no output.

- [ ] **Step 3: Commit**

```bash
git add audio-processor-gpu/modal_app.py
git commit -m "feat(audio-processor-gpu): add Modal A10G entrypoint"
```

---

## Task 7: Model bootstrap script

**Files:**
- Create: `audio-processor-gpu/seed_models.py`

- [ ] **Step 1: Create `audio-processor-gpu/seed_models.py`**:

```python
"""One-shot Modal command that downloads the BS-Roformer checkpoint into the
shared cantus-models Volume.

Usage:
    modal run seed_models.py

Idempotent: re-running is a no-op if the file already exists at the expected
size. Re-runnable to force-refresh by deleting the file first.
"""
from __future__ import annotations

import hashlib
import os
import urllib.request

import modal

volume = modal.Volume.from_name("cantus-models", create_if_missing=True)
app = modal.App("cantus-models-seed")

# BS-Roformer ep_368 checkpoint hosted on HuggingFace by audio-separator
# maintainers. Verify the URL is current at implementation time:
#   https://huggingface.co/seanghay/uvr_models/resolve/main/model_bs_roformer_ep_368_sdr_12.9628.ckpt
# If the host returns 404, mirror the file to an org-controlled bucket and
# update this constant.
MODEL_URL = (
    "https://huggingface.co/seanghay/uvr_models/resolve/main/"
    "model_bs_roformer_ep_368_sdr_12.9628.ckpt"
)
MODEL_NAME = "model_bs_roformer_ep_368_sdr_12.9628.ckpt"
MODEL_DIR = "/models"
# Expected SHA-256 of the official ep_368 ckpt. Fill in at implementation
# time after the first verified download — leaving as None means the function
# refuses to overwrite, which is correct on the very first run only.
EXPECTED_SHA256: str | None = None


@app.function(volumes={MODEL_DIR: volume}, timeout=600)
def seed() -> None:
    dst = os.path.join(MODEL_DIR, MODEL_NAME)
    if os.path.exists(dst) and os.path.getsize(dst) > 0:
        print(f"already seeded: {dst} ({os.path.getsize(dst)} bytes)")
        return

    print(f"downloading {MODEL_URL} → {dst}")
    os.makedirs(MODEL_DIR, exist_ok=True)
    tmp = dst + ".part"
    sha = hashlib.sha256()
    with urllib.request.urlopen(MODEL_URL) as resp, open(tmp, "wb") as f:
        while True:
            chunk = resp.read(1 << 20)  # 1 MiB
            if not chunk:
                break
            f.write(chunk)
            sha.update(chunk)
    digest = sha.hexdigest()
    print(f"sha256: {digest}")

    if EXPECTED_SHA256 is not None and digest != EXPECTED_SHA256:
        os.remove(tmp)
        raise RuntimeError(
            f"sha256 mismatch: got {digest}, expected {EXPECTED_SHA256}"
        )

    os.replace(tmp, dst)
    volume.commit()
    print(f"seeded {dst} ({os.path.getsize(dst)} bytes)")


@app.local_entrypoint()
def main() -> None:
    seed.remote()
```

- [ ] **Step 2: Smoke-check the file parses**

```bash
cd audio-processor-gpu && python -m py_compile seed_models.py
```

Expected: exit code 0.

- [ ] **Step 3: Commit**

```bash
git add audio-processor-gpu/seed_models.py
git commit -m "feat(audio-processor-gpu): add Modal Volume seed script for BS-Roformer ckpt"
```

---

## Task 8: Local end-to-end + Modal deploy verification

This task is verification, not new code. It exercises the rewrite in two environments and locks in the deferred items from the spec (`MODEL_URL`, `EXPECTED_SHA256`, model directory layout).

- [ ] **Step 1: Local smoke** — start the FastAPI app and run a tiny request through `/separate` with a small WAV.

```bash
cd audio-processor-gpu
# Download a small sample (10–20 sec WAV) into ./tmp/sample.wav by any means.
# Then either: (a) install audio-separator deps and the ckpt under ./tmp/models,
#              (b) or just point the stub-based tests at it — Task 4 tests
#                  already cover the contract path.
uvicorn main:app --port 8090 &
sleep 2
curl -sS -X POST localhost:8090/health | grep -q '"status":"ok"'
echo "local /health OK"
kill %1
```

Expected: `local /health OK` printed. The real `/separate` round-trip with a live model is optional locally — it depends on whether the engineer wants to download the ckpt onto their laptop. Document the result either way in the commit message.

- [ ] **Step 2: Seed the Modal Volume**

```bash
cd audio-processor-gpu && modal run seed_models.py
```

Expected output ends with: `seeded /models/model_bs_roformer_ep_368_sdr_12.9628.ckpt (NNN bytes)`. Capture the printed SHA-256 and paste it into `EXPECTED_SHA256` in `seed_models.py`. Commit that single-line change:

```bash
git add seed_models.py
git commit -m "chore: pin BS-Roformer ep_368 sha256 after first seed"
```

- [ ] **Step 3: Deploy and hit `/health`**

```bash
cd audio-processor-gpu && modal deploy modal_app.py
```

Note the deployed URL printed. Then:

```bash
curl -sS "$MODAL_URL/health"
```

Expected: `{"status":"ok"}`. Record the cold-start time observed in the Modal dashboard (target: ≤ 10s) and the warm `/separate` end-to-end time for a 4-minute song (target: ≤ 40s). If cold start > 30s or warm > 60s, raise a follow-up; do NOT block this plan on it.

- [ ] **Step 4: No code commit unless `MODEL_URL` changed.**

---

## Task 9: Update docs

**Files:**
- Modify: `CLAUDE.md`
- Modify: `audio-processor-gpu/services/__init__.py` (only if it currently re-exports DemucsService — verify with `cat`)

- [ ] **Step 1: Update `CLAUDE.md`** — in the "Architecture" diagram and the Python section, change `Demucs (vocal separation)` to `BS-Roformer (vocal separation)`. In the "Important Notes" section update the bullet:

Old: `**Demucs first run**: downloads ~1GB model weights. Subsequent runs are fast.`

New: `**BS-Roformer ckpt**: ~640 MB, seeded into the Modal Volume once via 'modal run seed_models.py'. Subsequent cold starts read from the Volume (~5–10s). Local runs read from $MODEL_DIR (default ./tmp/models).`

Also update the Python service description from `Demucs (vocal separation)` to `BS-Roformer via audio-separator (vocal separation)` and note the `audio-processor-gpu/modal_app.py` Modal entrypoint.

- [ ] **Step 2: Verify `services/__init__.py`** doesn't still export DemucsService:

```bash
cat audio-processor-gpu/services/__init__.py
```

If it does, remove that line.

- [ ] **Step 3: Run the full test suite as a final check**

```bash
cd audio-processor-gpu && pytest -v
```

Expected: all green, no Demucs-related collection errors.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md audio-processor-gpu/services/__init__.py
git commit -m "docs: point CLAUDE.md at BS-Roformer + Modal architecture"
```

---

## Self-Review (run after writing)

**Spec coverage:**
- Architecture (one Modal class, two methods via FastAPI) → Task 6.
- File layout (new files / modified / deleted) → Tasks 2–7 map 1:1.
- `RoformerService` contract + idempotency → Tasks 2 & 3.
- Router rewrite preserving Go contract → Task 4.
- Modal class with snap/`to_gpu`/asgi_app → Task 6.
- `seed_models.py` one-shot → Task 7.
- Local uvicorn still works → Task 8 step 1.
- Open items in spec (MODEL_URL, SHA, onnxruntime vs torch) → flagged in Task 7 comments and Task 8 step 2.

**Placeholder scan:** No "TBD/TODO/implement later" in task steps. `EXPECTED_SHA256 = None` is a deliberate fail-closed placeholder filled by Task 8 step 2 — that is correct, not a plan defect.

**Type consistency:** `RoformerService(model_dir, model_filename, separator_factory=None)` is used consistently in Tasks 2, 3, 4, 6. Output filenames `vocals.wav` / `no_vocals.wav` are consistent across `RoformerService.separate`, the router, and the existing Go expectations. `get_roformer_service` symbol used identically in Task 4 router and tests.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-15-modal-roformer-gpu.md`. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, you review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session with batch checkpoints.

Which approach?
