"""One-shot Modal command that pre-populates the cantus-models Volume with the
BS-Roformer checkpoint via `audio-separator`.

Why audio-separator does the download (not raw urllib): audio-separator owns the
model registry — it knows which exact .ckpt + companion config YAML to fetch
for a given model name, and where to put them in `model_file_dir`. Hand-rolling
URLs means we maintain those URLs ourselves and risk drift.

Usage:
    modal run seed_models.py

Idempotent: audio-separator skips download if files are already present.
"""

from __future__ import annotations

import modal

MODEL_NAME = "model_bs_roformer_ep_368_sdr_12.9628.ckpt"
MODEL_DIR = "/models"

volume = modal.Volume.from_name("cantus-models", create_if_missing=True)
app = modal.App("cantus-models-seed")

# Tiny CPU image — just enough to run audio-separator's model download.
# Mirrors requirements.txt's audio-separator pin so it loads the same registry.
seed_image = (
    modal.Image.debian_slim(python_version="3.12")
    .apt_install("ffmpeg")
    .pip_install(
        "audio-separator==0.30.0",
        "onnxruntime==1.20.1",
    )
)


@app.function(image=seed_image, volumes={MODEL_DIR: volume}, timeout=900)
def seed() -> None:
    from audio_separator.separator import Separator

    print(f"asking audio-separator to populate {MODEL_DIR} with {MODEL_NAME}")
    sep = Separator(model_file_dir=MODEL_DIR)
    sep.load_model(model_filename=MODEL_NAME)

    volume.commit()
    print("seed complete; volume committed")


@app.local_entrypoint()
def main() -> None:
    seed.remote()
