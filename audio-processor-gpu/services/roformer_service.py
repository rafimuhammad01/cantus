from __future__ import annotations

import os
from collections.abc import Callable
from pathlib import Path


def _default_factory(model_dir: str) -> Callable[[str], object]:
    def factory(output_dir: str) -> object:
        from audio_separator.separator import Separator

        return Separator(model_file_dir=model_dir, output_dir=output_dir)

    return factory


class RoformerService:
    """In-process BS-Roformer vocal separation.

    Wraps audio_separator.Separator. Renames the UVR-style output files to the
    canonical vocals.wav / no_vocals.wav expected by the rest of the pipeline.
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
        separator.load_model(model_filename=self._model_filename)

        try:
            written = separator.separate(input_path)
        except Exception as exc:
            raise RuntimeError(f"roformer failed: {exc}") from exc

        vocals_src = _pick(written, "Vocals")
        instr_src = _pick(written, "Instrumental")
        if vocals_src is None or instr_src is None:
            raise RuntimeError(f"roformer did not produce both stems; got {written!r}")
        os.replace(vocals_src, vocals_target)
        os.replace(instr_src, no_vocals_target)


def _pick(paths: list[str], kind: str) -> str | None:
    for p in paths:
        if kind in os.path.basename(p):
            return p
    return None
