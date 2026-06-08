from __future__ import annotations

import os
import subprocess
import tempfile
from collections.abc import Callable

import numpy as np
import pyrubberband
import soundfile


class PitchService:
    """Pitch-shifts an audio file by N semitones and transcodes to 128kbps MP3."""

    def __init__(
        self,
        runner: Callable[..., subprocess.CompletedProcess] = subprocess.run,
    ) -> None:
        self._runner = runner

    def shift(self, input_path: str, output_path: str, semitones: float) -> None:
        """Pitch-shift *input_path* by *semitones* and write a 128kbps MP3 to *output_path*.

        Raises:
            FileNotFoundError: if *input_path* does not exist.
            RuntimeError: if ffmpeg exits with a non-zero return code.
        """
        if not os.path.exists(input_path):
            raise FileNotFoundError(f"input_path not found: {input_path!r}")

        # Idempotency: skip if output already exists and is non-empty.
        if os.path.exists(output_path) and os.path.getsize(output_path) > 0:
            return

        out_dir = os.path.dirname(output_path) or "."
        tmp_wav: str | None = None
        tmp_fallback_wav: str | None = None
        tmp_mp3: str | None = None

        try:
            # --- 1. Read audio ---------------------------------------------------
            audio: np.ndarray
            sr: int
            try:
                audio, sr = soundfile.read(input_path)
            except soundfile.LibsndfileError:
                # libsndfile may not support MP3 — fall back to ffmpeg decode.
                with tempfile.NamedTemporaryFile(suffix=".wav", dir=out_dir, delete=False) as fh:
                    tmp_fallback_wav = fh.name
                self._run_ffmpeg(
                    [
                        "ffmpeg",
                        "-y",
                        "-i",
                        input_path,
                        "-ar",
                        "44100",
                        "-ac",
                        "2",
                        tmp_fallback_wav,
                    ]
                )
                audio, sr = soundfile.read(tmp_fallback_wav)

            # --- 2. Pitch-shift --------------------------------------------------
            # No -F (formant preservation) here: tested on full mixes and it produces
            # a doubling/phasing artifact because the polyphonic spectral envelope
            # doesn't fit the source-filter model rubberband assumes. Preview stage
            # accepts slight chipmunkiness; Group 7 will run -F on Demucs-isolated
            # vocals where the source-filter fit is valid.
            shifted: np.ndarray = pyrubberband.pitch_shift(audio, sr, semitones)

            # --- 3. Write shifted audio to a tmp WAV -----------------------------
            with tempfile.NamedTemporaryFile(suffix=".wav", dir=out_dir, delete=False) as fh:
                tmp_wav = fh.name
            soundfile.write(tmp_wav, shifted, sr)

            # --- 4. Transcode WAV → MP3 ------------------------------------------
            with tempfile.NamedTemporaryFile(suffix=".mp3", dir=out_dir, delete=False) as fh:
                tmp_mp3 = fh.name
            self._run_ffmpeg(
                [
                    "ffmpeg",
                    "-y",
                    "-i",
                    tmp_wav,
                    "-b:a",
                    "128k",
                    "-ar",
                    "44100",
                    tmp_mp3,
                ]
            )

            # --- 5. Atomic rename ------------------------------------------------
            os.replace(tmp_mp3, output_path)
            tmp_mp3 = None  # consumed — don't delete in finally

        finally:
            for path in (tmp_wav, tmp_fallback_wav, tmp_mp3):
                if path is not None and os.path.exists(path):
                    try:
                        os.unlink(path)
                    except OSError:
                        pass

    def _run_ffmpeg(self, cmd: list[str]) -> None:
        result = self._runner(cmd, capture_output=True)
        if result.returncode != 0:
            stderr_tail = result.stderr[-500:].decode(errors="replace") if result.stderr else ""
            raise RuntimeError(f"ffmpeg failed: {stderr_tail}")
