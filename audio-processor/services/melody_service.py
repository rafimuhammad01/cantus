from __future__ import annotations

import json
import os
from collections.abc import Callable

import crepe
import librosa

STEP_SIZE_MS: int = 50
CONF_THRESHOLD: float = 0.60
ENERGY_THRESHOLD: float = 0.015

_REQUIRED_KEYS: frozenset[str] = frozenset({"hop_ms", "min_hz", "max_hz", "frames"})


class MelodyService:
    """Extracts a pitch timeline from an isolated vocals stem via CREPE."""

    def __init__(
        self,
        predictor: Callable[..., tuple] = crepe.predict,
        loader: Callable[..., tuple] = librosa.load,
    ) -> None:
        self._predict = predictor
        self._load = loader

    def extract(self, vocals_path: str, output_path: str) -> None:
        """Run CREPE on vocals_path and write melody JSON to output_path.

        Raises:
            FileNotFoundError: if vocals_path does not exist.
            RuntimeError: if CREPE/librosa fail or output is empty.
        """
        if not os.path.exists(vocals_path):
            raise FileNotFoundError(f"vocals_path not found: {vocals_path!r}")

        # Idempotency: skip if output exists and is a valid melody JSON.
        if os.path.exists(output_path):
            try:
                with open(output_path) as fh:
                    existing = json.load(fh)
                if _REQUIRED_KEYS.issubset(existing.keys()):
                    return
            except (json.JSONDecodeError, OSError):
                pass  # corrupted — fall through and re-run

        audio, sr = self._load(vocals_path, sr=16000)

        hop_len = int(sr * STEP_SIZE_MS / 1000)
        rms = librosa.feature.rms(y=audio, frame_length=1024, hop_length=hop_len)[0]

        times, freqs, conf, _ = self._predict(
            audio,
            sr,
            model_capacity="tiny",
            step_size=STEP_SIZE_MS,
            viterbi=True,
        )

        n = min(len(times), len(rms))

        voiced_hz: list[float] = []
        frames: list[list[int | float]] = []
        for i in range(n):
            t_ms = int(round(float(times[i]) * 1000))
            voiced = (
                float(conf[i]) > CONF_THRESHOLD
                and float(rms[i]) > ENERGY_THRESHOLD
                and float(freqs[i]) > 0
            )
            hz = float(freqs[i]) if voiced else 0.0
            if voiced:
                voiced_hz.append(hz)
            frames.append([t_ms, hz])

        min_hz = min(voiced_hz) if voiced_hz else 0.0
        max_hz = max(voiced_hz) if voiced_hz else 0.0

        payload = {
            "hop_ms": STEP_SIZE_MS,
            "min_hz": min_hz,
            "max_hz": max_hz,
            "frames": frames,
        }

        out_dir = os.path.dirname(output_path)
        if out_dir:
            os.makedirs(out_dir, exist_ok=True)

        tmp_path = output_path + ".tmp"
        with open(tmp_path, "w") as fh:
            json.dump(payload, fh)
        os.replace(tmp_path, output_path)
