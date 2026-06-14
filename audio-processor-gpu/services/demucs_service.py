from __future__ import annotations

import os
from pathlib import Path

import soundfile as sf
import torch

# Separator is loaded once per process; reuse across requests is intentional.
# Constructing DemucsService is expensive (~80 MB model load for htdemucs); the
# @lru_cache(maxsize=1) singleton in routers/separate.py ensures it happens
# only on the first /separate request and is then held for the process lifetime.
#
# NOTE: we don't use demucs.api.Separator because it isn't shipped in the
# demucs==4.0.1 PyPI wheel (only on the main branch on GitHub). Instead we wrap
# the lower-level demucs.pretrained + demucs.apply + demucs.audio APIs in a
# Separator-shaped class so tests can stay identical to the public API contract.
#
# Current quality settings: model="htdemucs" (single model, fastest), shifts=1
# (no time-shift averaging). Quality knobs to revisit later when wiring up the
# karaoke vocal-guide track: "htdemucs_ft" (4-model ensemble, ~4× slower but
# noticeably less bleed) and shifts=2..5 (further halves/quarters bleed at
# proportional cost).


class InProcessSeparator:
    """Duck-types demucs.api.Separator using demucs's lower-level APIs.

    Loads the model on construction and reuses it for every separate_audio_file call.
    """

    def __init__(
        self,
        model_name: str = "htdemucs",
        shifts: int = 1,
        device: str = "cpu",
    ) -> None:
        from demucs.pretrained import get_model

        model = get_model(model_name)
        model.to(device)
        model.eval()
        self._model = model
        self._shifts = shifts
        self._device = device
        self.samplerate: int = model.samplerate

    def separate_audio_file(self, path: Path) -> tuple[torch.Tensor, dict[str, torch.Tensor]]:
        """Match the demucs.api.Separator.separate_audio_file signature."""
        from demucs.apply import apply_model
        from demucs.audio import AudioFile

        wav = AudioFile(path).read(
            streams=0,
            samplerate=self._model.samplerate,
            channels=self._model.audio_channels,
        )

        # Per-channel normalization matches what demucs.separate.main does internally.
        ref = wav.mean(0)
        wav = (wav - ref.mean()) / (ref.std() + 1e-8)
        wav = wav.to(self._device)

        with torch.no_grad():
            sources = apply_model(
                self._model,
                wav[None],
                shifts=self._shifts,
                split=True,
                overlap=0.25,
                device=self._device,
            )[0]

        sources = sources * ref.std() + ref.mean()
        sources = sources.cpu()
        stems = dict(zip(self._model.sources, sources, strict=True))
        return wav.cpu(), stems


class DemucsService:
    """In-process Demucs separation; holds a loaded Separator for the process lifetime."""

    def __init__(
        self,
        device: str = "cpu",
        separator: object | None = None,
    ) -> None:
        if separator is None:
            separator = InProcessSeparator(device=device)
        self._separator = separator

    def separate(self, input_path: str, output_dir: str) -> None:
        """Run Demucs on input_path and write vocals.wav + no_vocals.wav under output_dir.

        Raises:
            FileNotFoundError: if input_path does not exist.
            RuntimeError: if separation fails for any reason.
        """
        if not os.path.exists(input_path):
            raise FileNotFoundError(f"input_path not found: {input_path!r}")

        vocals_target = os.path.join(output_dir, "vocals.wav")
        no_vocals_target = os.path.join(output_dir, "no_vocals.wav")

        # Idempotency: both targets present and non-empty → nothing to do.
        if (
            os.path.exists(vocals_target)
            and os.path.getsize(vocals_target) > 0
            and os.path.exists(no_vocals_target)
            and os.path.getsize(no_vocals_target) > 0
        ):
            return

        os.makedirs(output_dir, exist_ok=True)

        try:
            _audio, stems = self._separator.separate_audio_file(Path(input_path))
        except Exception as exc:
            raise RuntimeError(f"demucs failed: {exc}") from exc

        vocals_tensor = stems["vocals"]
        no_vocals_tensor = stems["drums"] + stems["bass"] + stems["other"]

        sr = self._separator.samplerate

        for tensor, target in [
            (vocals_tensor, vocals_target),
            (no_vocals_tensor, no_vocals_target),
        ]:
            tmp = target + ".tmp"
            # soundfile expects (samples, channels) as numpy; tensor is (channels, samples).
            arr = tensor.cpu().numpy().T
            sf.write(tmp, arr, samplerate=sr, subtype="PCM_16", format="WAV")
            os.replace(tmp, target)
