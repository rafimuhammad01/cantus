from __future__ import annotations

from pathlib import Path

import pytest
import torch
import torchaudio

from services.demucs_service import DemucsService

# ---------------------------------------------------------------------------
# Fake Separator
# ---------------------------------------------------------------------------


class FakeSeparator:
    """Duck-types demucs.api.Separator for tests."""

    samplerate: int = 44100

    def __init__(
        self,
        stems: dict[str, torch.Tensor] | None = None,
        raises: Exception | None = None,
    ) -> None:
        self._stems = stems or {
            "vocals": torch.tensor([[0.1, 0.2, 0.3, 0.4]]),  # (1 channel, 4 samples)
            "drums": torch.tensor([[0.01, 0.02, 0.03, 0.04]]),
            "bass": torch.tensor([[0.001, 0.002, 0.003, 0.004]]),
            "other": torch.tensor([[0.0001, 0.0002, 0.0003, 0.0004]]),
        }
        self._raises = raises
        self.call_count = 0
        self.last_path: Path | None = None

    def separate_audio_file(self, path: Path) -> tuple[torch.Tensor, dict[str, torch.Tensor]]:
        """Return (origin, stems) like the real API."""
        self.call_count += 1
        self.last_path = path
        if self._raises is not None:
            raise self._raises
        return torch.zeros_like(self._stems["vocals"]), self._stems


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_separate_happy_path_writes_both_stems(tmp_path: Path) -> None:
    """Input file exists; fake Separator returns known stems; both WAVs are written."""
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"dummy audio bytes")
    output_dir = str(tmp_path / "out")

    fake = FakeSeparator()
    service = DemucsService(separator=fake)
    service.separate(str(input_file), output_dir)

    vocals_path = Path(output_dir, "vocals.wav")
    no_vocals_path = Path(output_dir, "no_vocals.wav")
    assert vocals_path.exists(), "vocals.wav must exist in output_dir"
    assert no_vocals_path.exists(), "no_vocals.wav must exist in output_dir"

    waveform, sr = torchaudio.load(str(vocals_path))
    assert sr == 44100, f"expected sample rate 44100, got {sr}"

    # Vocals tensor round-trips within int16 quantisation tolerance.
    original = fake._stems["vocals"].cpu().float()
    loaded = waveform.float()
    assert torch.allclose(
        loaded, original, atol=1e-3
    ), f"vocals tensor did not round-trip: max diff {(loaded - original).abs().max()}"


def test_separate_no_vocals_is_sum_of_drums_bass_other(tmp_path: Path) -> None:
    """no_vocals.wav must equal drums + bass + other (within int16 tolerance)."""
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"dummy audio bytes")
    output_dir = str(tmp_path / "out")

    stems = {
        "vocals": torch.full((1, 4), 0.5),
        "drums": torch.full((1, 4), 0.1),
        "bass": torch.full((1, 4), 0.2),
        "other": torch.full((1, 4), 0.05),
    }
    fake = FakeSeparator(stems=stems)
    service = DemucsService(separator=fake)
    service.separate(str(input_file), output_dir)

    waveform, sr = torchaudio.load(str(Path(output_dir, "no_vocals.wav")))
    expected = stems["drums"] + stems["bass"] + stems["other"]
    assert torch.allclose(
        waveform.float(), expected.float(), atol=1e-3
    ), f"no_vocals mismatch: max diff {(waveform.float() - expected.float()).abs().max()}"


def test_separate_idempotency_skips_when_both_stems_exist(tmp_path: Path) -> None:
    """Both stems already present and non-empty → separator must NOT be called."""
    output_dir = tmp_path / "out"
    output_dir.mkdir()
    (output_dir / "vocals.wav").write_bytes(b"existing-vocals")
    (output_dir / "no_vocals.wav").write_bytes(b"existing-no-vocals")

    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")

    fake = FakeSeparator(raises=AssertionError("should not run"))
    service = DemucsService(separator=fake)
    service.separate(str(input_file), str(output_dir))

    assert fake.call_count == 0, "separator must not be called when both stems exist"
    assert (output_dir / "vocals.wav").read_bytes() == b"existing-vocals"
    assert (output_dir / "no_vocals.wav").read_bytes() == b"existing-no-vocals"


def test_separate_partial_cache_reruns(tmp_path: Path) -> None:
    """Only vocals.wav present → separator IS called; both files have post-run content."""
    output_dir = tmp_path / "out"
    output_dir.mkdir()
    (output_dir / "vocals.wav").write_bytes(b"stale")  # only vocals, no no_vocals

    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")

    fake = FakeSeparator()
    service = DemucsService(separator=fake)
    service.separate(str(input_file), str(output_dir))

    assert fake.call_count == 1, "separator must be called when cache is partial"
    assert (output_dir / "vocals.wav").stat().st_size > len(
        b"stale"
    ), "vocals.wav should have been overwritten with proper WAV content"
    assert (output_dir / "no_vocals.wav").exists()


def test_separate_missing_input_raises_file_not_found(tmp_path: Path) -> None:
    """Non-existent input_path → FileNotFoundError; separator not called."""
    fake = FakeSeparator()
    service = DemucsService(separator=fake)

    with pytest.raises(FileNotFoundError):
        service.separate(str(tmp_path / "does_not_exist.wav"), str(tmp_path / "out"))

    assert fake.call_count == 0, "separator must not be called when input is missing"


def test_separate_separator_exception_becomes_runtime_error(tmp_path: Path) -> None:
    """Exception from separator.separate_audio_file → RuntimeError containing 'demucs failed'."""
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")

    fake = FakeSeparator(raises=Exception("MPS out of memory"))
    service = DemucsService(separator=fake)

    with pytest.raises(RuntimeError) as exc_info:
        service.separate(str(input_file), str(tmp_path / "out"))

    msg = str(exc_info.value)
    assert "demucs failed" in msg.lower(), f"expected 'demucs failed' in: {msg}"
    assert "MPS out of memory" in msg, f"expected original message in: {msg}"


def test_separate_no_leftover_tmp_files(tmp_path: Path) -> None:
    """After a successful run, no .tmp files must remain in output_dir."""
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")
    output_dir = str(tmp_path / "out")

    fake = FakeSeparator()
    service = DemucsService(separator=fake)
    service.separate(str(input_file), output_dir)

    tmp_files = list(Path(output_dir).glob("*.tmp"))
    assert tmp_files == [], f"leftover .tmp files found: {tmp_files}"


def test_separate_creates_output_directory(tmp_path: Path) -> None:
    """output_dir that doesn't yet exist must be created during separation."""
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")
    output_dir = tmp_path / "nested" / "out"  # does not exist

    fake = FakeSeparator()
    service = DemucsService(separator=fake)
    service.separate(str(input_file), str(output_dir))

    assert (output_dir / "vocals.wav").exists()
    assert (output_dir / "no_vocals.wav").exists()
