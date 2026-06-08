from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from services.melody_service import MelodyService

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

STEP_SIZE_MS = 50  # must match melody_service constant


def make_fake_predictor(
    times: list[float],
    freqs: list[float],
    conf: list[float],
) -> Any:
    """Return a callable matching crepe.predict's signature."""

    def fake(audio: np.ndarray, sr: int, **kwargs: object) -> tuple:
        return np.asarray(times), np.asarray(freqs), np.asarray(conf), None

    return fake


def make_fake_loader(audio: np.ndarray, sr: int = 16000) -> Any:
    """Return a callable matching librosa.load's signature."""

    def fake(path: str, sr: int | None = None) -> tuple[np.ndarray, int]:
        return audio, 16000

    return fake


def _voiced_audio(n_samples: int = 8000) -> np.ndarray:
    """0.5s of noise at amplitude 0.1 — RMS will be > 0.015 in every frame."""
    rng = np.random.default_rng(42)
    return (rng.random(n_samples) * 0.2 - 0.1).astype(np.float32)


def _silent_audio(n_samples: int = 8000) -> np.ndarray:
    """Amplitude 1e-4 → RMS < 0.015."""
    return np.full(n_samples, 1e-4, dtype=np.float32)


def _mixed_audio_with_one_silent_segment() -> np.ndarray:
    """~0.2s voiced then ~50ms silence then voiced again (at 16kHz)."""
    voiced = _voiced_audio(3200)
    silent = _silent_audio(800)
    return np.concatenate([voiced, silent, voiced])


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_extract_happy_path_writes_compact_json(tmp_path: Path) -> None:
    """Happy path: mixed voiced/unvoiced input → compact JSON with correct schema and values."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"

    audio = _voiced_audio(8000)  # 0.5s @ 16kHz; RMS > 0.015 in all frames
    times = [0.0, 0.05, 0.10, 0.15]
    freqs = [440.0, 440.0, 0.0, 880.0]
    conf = [0.8, 0.8, 0.2, 0.9]

    service = MelodyService(
        predictor=make_fake_predictor(times, freqs, conf),
        loader=make_fake_loader(audio),
    )
    service.extract(str(vocals), str(output))

    assert output.exists()
    data = json.loads(output.read_text())

    # Schema: exactly these top-level keys
    assert set(data.keys()) == {"hop_ms", "min_hz", "max_hz", "frames"}
    assert data["hop_ms"] == STEP_SIZE_MS

    # frames: conf=0.2 → unvoiced (hz=0.0); conf=0.9 with freq=880 → voiced
    expected_frames = [[0, 440.0], [50, 440.0], [100, 0.0], [150, 880.0]]
    assert data["frames"] == expected_frames

    assert data["min_hz"] == 440.0
    assert data["max_hz"] == 880.0


def test_extract_energy_gate_drops_silent_frame_even_with_high_conf(tmp_path: Path) -> None:
    """Energy gate: high-confidence frame in silence region must output hz=0.0.

    librosa.feature.rms uses center-padding with frame_length=1024, so adjacent voiced
    frames bleed into a 1-hop silent segment. We use 2 hops (1600 samples) of silence so
    the center of frame index 3 is entirely within silence (RMS < 0.015).
    Audio layout: voiced | voiced | silent | silent | voiced | voiced (each = 800 samples).
    CREPE times map each hop to one frame: indices 0-5.
    """
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"

    hop = 800  # 50ms @ 16kHz
    voiced_chunk = _voiced_audio(hop)
    silent_chunk = _silent_audio(hop)
    # 2 voiced | 2 silent | 2 voiced — frame index 3 (t=150ms) is the second silent hop
    audio = np.concatenate(
        [
            voiced_chunk,
            voiced_chunk,
            silent_chunk,
            silent_chunk,
            voiced_chunk,
            voiced_chunk,
        ]
    )

    # Frame index 3 (t=150ms): the center-padded RMS frame falls fully in silence → < 0.015
    times = [0.0, 0.05, 0.10, 0.15, 0.20, 0.25]
    freqs = [440.0, 440.0, 440.0, 330.0, 440.0, 440.0]
    conf = [0.9, 0.9, 0.9, 0.95, 0.9, 0.9]  # all high conf — energy gate is what fires

    service = MelodyService(
        predictor=make_fake_predictor(times, freqs, conf),
        loader=make_fake_loader(audio),
    )
    service.extract(str(vocals), str(output))

    data = json.loads(output.read_text())
    frames = data["frames"]

    # Frame at t=150ms (index 3) must be 0.0 despite high conf — energy gate fired
    frame_at_150 = next(f for f in frames if f[0] == 150)
    assert frame_at_150[1] == 0.0, f"expected 0.0 (energy gate), got {frame_at_150[1]}"


def test_extract_all_unvoiced_returns_zero_min_max(tmp_path: Path) -> None:
    """All-unvoiced output → min_hz=0.0, max_hz=0.0, all frames hz=0.0."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"

    audio = _voiced_audio(4000)
    times = [0.0, 0.05, 0.10]
    freqs = [0.0, 0.0, 0.0]
    conf = [0.1, 0.1, 0.1]  # below CONF_THRESHOLD

    service = MelodyService(
        predictor=make_fake_predictor(times, freqs, conf),
        loader=make_fake_loader(audio),
    )
    service.extract(str(vocals), str(output))

    data = json.loads(output.read_text())
    assert data["min_hz"] == 0.0
    assert data["max_hz"] == 0.0
    assert all(f[1] == 0.0 for f in data["frames"])


def test_extract_idempotency_skips_when_output_valid(tmp_path: Path) -> None:
    """Valid output file already present → predictor/loader NOT called, file unchanged."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"

    existing = {
        "hop_ms": 50,
        "min_hz": 200.0,
        "max_hz": 600.0,
        "frames": [[0, 200.0], [50, 0.0]],
    }
    output.write_text(json.dumps(existing))

    def should_not_run(*args: object, **kwargs: object) -> None:
        raise AssertionError("predictor/loader should not have been called")

    service = MelodyService(
        predictor=should_not_run,
        loader=should_not_run,
    )
    service.extract(str(vocals), str(output))

    # File must be unchanged
    assert json.loads(output.read_text()) == existing


def test_extract_idempotency_overwrites_when_output_corrupted(tmp_path: Path) -> None:
    """Corrupted (non-JSON) output file → re-run and overwrite with valid content."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"
    output.write_text("not json")

    audio = _voiced_audio(4000)
    times = [0.0, 0.05]
    freqs = [440.0, 0.0]
    conf = [0.9, 0.1]

    service = MelodyService(
        predictor=make_fake_predictor(times, freqs, conf),
        loader=make_fake_loader(audio),
    )
    service.extract(str(vocals), str(output))

    data = json.loads(output.read_text())
    assert set(data.keys()) == {"hop_ms", "min_hz", "max_hz", "frames"}


def test_extract_idempotency_overwrites_when_schema_missing_keys(tmp_path: Path) -> None:
    """Partial schema (missing min_hz/max_hz/frames) → re-run and overwrite."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"
    output.write_text(json.dumps({"hop_ms": 50}))  # missing required keys

    audio = _voiced_audio(4000)
    times = [0.0, 0.05]
    freqs = [440.0, 880.0]
    conf = [0.9, 0.9]

    service = MelodyService(
        predictor=make_fake_predictor(times, freqs, conf),
        loader=make_fake_loader(audio),
    )
    service.extract(str(vocals), str(output))

    data = json.loads(output.read_text())
    assert set(data.keys()) == {"hop_ms", "min_hz", "max_hz", "frames"}
    assert len(data["frames"]) == 2


def test_extract_missing_input_raises_file_not_found(tmp_path: Path) -> None:
    """Non-existent vocals_path → FileNotFoundError; predictor NOT called."""
    called: list[bool] = []

    def should_not_run(*args: object, **kwargs: object) -> None:
        called.append(True)
        raise AssertionError("should not run")

    service = MelodyService(predictor=should_not_run, loader=should_not_run)

    with pytest.raises(FileNotFoundError):
        service.extract(str(tmp_path / "missing.wav"), str(tmp_path / "out.json"))

    assert not called


def test_extract_creates_output_directory(tmp_path: Path) -> None:
    """output_path inside a non-existent subdirectory → directory is created automatically."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "nested" / "deep" / "melody.json"

    audio = _voiced_audio(4000)
    times = [0.0]
    freqs = [440.0]
    conf = [0.9]

    service = MelodyService(
        predictor=make_fake_predictor(times, freqs, conf),
        loader=make_fake_loader(audio),
    )
    service.extract(str(vocals), str(output))

    assert output.exists()
    data = json.loads(output.read_text())
    assert "frames" in data


def test_extract_passes_correct_crepe_args(tmp_path: Path) -> None:
    """predictor must be called with model_capacity='tiny', step_size=50, viterbi=True."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"

    recorded_kwargs: list[dict] = []
    audio = _voiced_audio(4000)

    def recording_predictor(audio_in: np.ndarray, sr: int, **kwargs: object) -> tuple:
        recorded_kwargs.append(dict(kwargs))
        return np.array([0.0]), np.array([440.0]), np.array([0.9]), None

    service = MelodyService(
        predictor=recording_predictor,
        loader=make_fake_loader(audio),
    )
    service.extract(str(vocals), str(output))

    assert recorded_kwargs, "predictor must have been called"
    kw = recorded_kwargs[0]
    assert kw.get("model_capacity") == "tiny"
    assert kw.get("step_size") == STEP_SIZE_MS
    assert kw.get("viterbi") is True


def test_extract_passes_sr_16000_to_loader(tmp_path: Path) -> None:
    """loader must be called with sr=16000."""
    vocals = tmp_path / "vocals.wav"
    vocals.write_bytes(b"fake")
    output = tmp_path / "melody.json"

    recorded: list[dict] = []

    def recording_loader(path: str, sr: int | None = None) -> tuple[np.ndarray, int]:
        recorded.append({"path": path, "sr": sr})
        return _voiced_audio(4000), 16000

    service = MelodyService(
        predictor=make_fake_predictor([0.0], [440.0], [0.9]),
        loader=recording_loader,
    )
    service.extract(str(vocals), str(output))

    assert recorded, "loader must have been called"
    assert recorded[0]["sr"] == 16000
