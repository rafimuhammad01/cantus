from __future__ import annotations

import os
import subprocess

import numpy as np
import pytest
import soundfile as sf

from services.pitch_service import PitchService

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_SR = 44100
_DURATION = 1  # seconds


def _make_sine_wav(path: str, frequency: float = 440.0, sr: int = _SR) -> None:
    """Write a 1-second mono sine wave at *frequency* Hz to *path* as WAV."""
    t = np.linspace(0, _DURATION, _SR * _DURATION, endpoint=False)
    audio = (np.sin(2 * np.pi * frequency * t) * 0.5).astype(np.float32)
    sf.write(path, audio, sr)


def _dominant_frequency(path: str) -> float:
    """Decode audio at *path* (via soundfile) and return the FFT dominant freq."""
    audio, sr = sf.read(path)
    if audio.ndim > 1:
        audio = audio[:, 0]
    spectrum = np.abs(np.fft.rfft(audio))
    freqs = np.fft.rfftfreq(len(audio), d=1.0 / sr)
    return float(freqs[np.argmax(spectrum)])


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_shift_zero_semitones_produces_nonempty_mp3(tmp_path: pytest.TempdirFactory) -> None:
    """Pipeline end-to-end: shift by 0 semitones, output MP3 must be non-empty."""
    input_wav = str(tmp_path / "input.wav")
    output_mp3 = str(tmp_path / "output.mp3")
    _make_sine_wav(input_wav)

    PitchService().shift(input_wav, output_mp3, semitones=0.0)

    assert os.path.exists(output_mp3), "output file must exist"
    assert os.path.getsize(output_mp3) > 1000, "output file must be a non-trivial MP3"


def test_shift_octave_up_dominates_at_880hz(tmp_path: pytest.TempdirFactory) -> None:
    """+12 semitones shifts 440 Hz tone to ~880 Hz (within 5%)."""
    input_wav = str(tmp_path / "input.wav")
    output_mp3 = str(tmp_path / "output.mp3")
    # Write a WAV so soundfile can decode the output for FFT analysis
    output_wav = str(tmp_path / "output_check.wav")
    _make_sine_wav(input_wav, frequency=440.0)

    PitchService().shift(input_wav, output_mp3, semitones=12.0)

    # Transcode MP3 back to WAV for FFT (ffmpeg on path in CI)
    result = subprocess.run(
        ["ffmpeg", "-y", "-i", output_mp3, "-ar", str(_SR), output_wav],
        capture_output=True,
    )
    assert result.returncode == 0, f"ffmpeg decode failed: {result.stderr.decode()[:200]}"

    dominant = _dominant_frequency(output_wav)
    target = 880.0
    assert (
        abs(dominant - target) / target < 0.05
    ), f"Expected dominant freq near {target} Hz, got {dominant:.1f} Hz"


def test_shift_idempotency_does_not_overwrite_existing_output(
    tmp_path: pytest.TempdirFactory,
) -> None:
    """If output_path already exists and is non-empty, shift() must return immediately."""
    input_wav = str(tmp_path / "input.wav")
    output_mp3 = str(tmp_path / "output.mp3")
    _make_sine_wav(input_wav)

    # Pre-create output with known garbage content
    garbage = b"X" * 100
    with open(output_mp3, "wb") as f:
        f.write(garbage)

    PitchService().shift(input_wav, output_mp3, semitones=2.0)

    # File must be untouched
    assert os.path.getsize(output_mp3) == 100
    with open(output_mp3, "rb") as f:
        assert f.read() == garbage


def test_shift_missing_input_raises_file_not_found(tmp_path: pytest.TempdirFactory) -> None:
    """Missing input_path → FileNotFoundError."""
    input_wav = str(tmp_path / "does_not_exist.wav")
    output_mp3 = str(tmp_path / "output.mp3")

    with pytest.raises(FileNotFoundError):
        PitchService().shift(input_wav, output_mp3, semitones=0.0)


def test_shift_ffmpeg_failure_raises_runtime_error(tmp_path: pytest.TempdirFactory) -> None:
    """Injected ffmpeg runner returning non-zero exit → RuntimeError with 'ffmpeg' in message."""
    input_wav = str(tmp_path / "input.wav")
    output_mp3 = str(tmp_path / "output.mp3")
    _make_sine_wav(input_wav)

    def _fake_runner(cmd: list[str], **kwargs: object) -> subprocess.CompletedProcess:
        # Distinguish ffmpeg calls (first arg is "ffmpeg") from any other subprocess call
        if cmd and cmd[0] == "ffmpeg":
            return subprocess.CompletedProcess(
                args=cmd, returncode=1, stdout=b"", stderr=b"ffmpeg boom"
            )
        # Shouldn't be called with anything else in these tests, but forward just in case
        return subprocess.run(cmd, **kwargs)  # type: ignore[arg-type]

    service = PitchService(runner=_fake_runner)

    with pytest.raises(RuntimeError, match="ffmpeg"):
        service.shift(input_wav, output_mp3, semitones=0.0)
