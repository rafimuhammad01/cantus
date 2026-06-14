"""One-shot Modal command that downloads the BS-Roformer checkpoint into the
shared cantus-models Volume.

Usage:
    modal run seed_models.py

Idempotent: re-running is a no-op if the file already exists.
"""

from __future__ import annotations

import hashlib
import os
import urllib.request

import modal

volume = modal.Volume.from_name("cantus-models", create_if_missing=True)
app = modal.App("cantus-models-seed")

# BS-Roformer ep_368 checkpoint. Verify URL is reachable at deploy time; if it
# 404s, mirror to an org bucket and update this constant.
MODEL_URL = (
    "https://huggingface.co/seanghay/uvr_models/resolve/main/"
    "model_bs_roformer_ep_368_sdr_12.9628.ckpt"
)
MODEL_NAME = "model_bs_roformer_ep_368_sdr_12.9628.ckpt"
MODEL_DIR = "/models"
# Pin after first verified download.
EXPECTED_SHA256: str | None = None


@app.function(volumes={MODEL_DIR: volume}, timeout=600)
def seed() -> None:
    dst = os.path.join(MODEL_DIR, MODEL_NAME)
    if os.path.exists(dst) and os.path.getsize(dst) > 0:
        print(f"already seeded: {dst} ({os.path.getsize(dst)} bytes)")
        return

    print(f"downloading {MODEL_URL} -> {dst}")
    os.makedirs(MODEL_DIR, exist_ok=True)
    tmp = dst + ".part"
    sha = hashlib.sha256()
    with urllib.request.urlopen(MODEL_URL) as resp, open(tmp, "wb") as f:
        while True:
            chunk = resp.read(1 << 20)
            if not chunk:
                break
            f.write(chunk)
            sha.update(chunk)
    digest = sha.hexdigest()
    print(f"sha256: {digest}")

    if EXPECTED_SHA256 is not None and digest != EXPECTED_SHA256:
        os.remove(tmp)
        raise RuntimeError(f"sha256 mismatch: got {digest}, expected {EXPECTED_SHA256}")

    os.replace(tmp, dst)
    volume.commit()
    print(f"seeded {dst} ({os.path.getsize(dst)} bytes)")


@app.local_entrypoint()
def main() -> None:
    seed.remote()
