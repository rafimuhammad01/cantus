"""Modal entrypoint for the Cantus audio-processor-gpu service.

Deploys the existing FastAPI app (main.app) on Modal A10G with:
- BS-Roformer + CREPE weights loaded from a persistent Volume,
- memory snapshot after model load → ~5–10s cold start,
- scale-to-zero so the $30/mo Modal credit stays intact.
"""

from __future__ import annotations

import os

import modal

image = (
    modal.Image.debian_slim(python_version="3.12")
    .apt_install("ffmpeg")
    # crepe 0.0.16 is an sdist whose setup.py imports pkg_resources, which
    # setuptools dropped in v80+. Install legacy setuptools first, then build
    # crepe without build isolation so it picks it up.
    .pip_install("setuptools<80", "wheel")
    .pip_install("crepe==0.0.16", extra_options="--no-build-isolation")
    .pip_install_from_requirements("requirements.txt")
    .env({"MODEL_DIR": "/models"})
    # Ship our source modules into the container so main, routers, services
    # are importable at runtime.
    .add_local_python_source("main", "logging_config", "routers", "services")
)

volume = modal.Volume.from_name("cantus-models", create_if_missing=True)
app = modal.App("cantus-audio-processor")


@app.cls(
    gpu="L40S",
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
        from routers.melody import get_melody_service
        from routers.separate import get_roformer_service

        self._roformer = get_roformer_service()
        self._melody = get_melody_service()

    @modal.enter(snap=False)
    def to_gpu(self) -> None:
        os.environ["CUDA_VISIBLE_DEVICES"] = "0"

    @modal.asgi_app()
    def fastapi_app(self):
        from main import app as fastapi

        return fastapi
