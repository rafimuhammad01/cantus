from __future__ import annotations

from pathlib import Path

from services.roformer_service import RoformerService


class FakeSeparator:
    """Duck-types audio_separator.Separator."""

    def __init__(self) -> None:
        self.output_dir: str | None = None
        self.loaded_model: str | None = None
        self.last_input: str | None = None

    def load_model(self, model_filename: str) -> None:
        self.loaded_model = model_filename

    def separate(self, input_path: str) -> list[str]:
        self.last_input = input_path
        assert self.output_dir is not None
        v = Path(self.output_dir) / "track_(Vocals)_BS-Roformer.mp3"
        i = Path(self.output_dir) / "track_(Instrumental)_BS-Roformer.mp3"
        v.write_bytes(b"VOCALS-MP3")
        i.write_bytes(b"INSTR-MP3")
        return [str(v), str(i)]


def test_separate_happy_path(tmp_path: Path) -> None:
    input_file = tmp_path / "track.wav"
    input_file.write_bytes(b"audio")
    output_dir = tmp_path / "out"

    fake = FakeSeparator()

    def factory(out: str) -> FakeSeparator:
        fake.output_dir = out
        return fake

    service = RoformerService(
        model_dir="/unused",
        model_filename="model_bs_roformer_ep_368_sdr_12.9628.ckpt",
        separator_factory=factory,
    )
    service.separate(str(input_file), str(output_dir))

    assert (output_dir / "vocals.mp3").read_bytes() == b"VOCALS-MP3"
    assert (output_dir / "no_vocals.mp3").read_bytes() == b"INSTR-MP3"
    assert fake.loaded_model == "model_bs_roformer_ep_368_sdr_12.9628.ckpt"
