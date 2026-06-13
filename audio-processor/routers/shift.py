from __future__ import annotations

import asyncio
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp, upload_from_path
from services.pitch_service import PitchService


class ShiftRequest(BaseModel):
    input_url: str = Field(min_length=1)
    output_url: str = Field(min_length=1)
    semitones: float = Field(ge=-12, le=12)


@lru_cache(maxsize=1)
def get_pitch_service() -> PitchService:
    """Singleton PitchService. Tests override via app.dependency_overrides."""
    return PitchService()


ShiftServiceDep = Annotated[PitchService, Depends(get_pitch_service)]

router = APIRouter()


@router.post("/shift")
def shift(req: ShiftRequest, service: ShiftServiceDep) -> dict:
    """Download input_url → shift locally → upload to output_url. Synchronous
    HTTP handler that internally drives the async io helpers via asyncio.run."""

    async def _run() -> None:
        with tempfile.TemporaryDirectory(prefix="shift-") as td:
            scratch = Path(td)
            src = await download_to_temp(req.input_url, scratch)
            dst = scratch / "out.mp3"
            try:
                service.shift(str(src), str(dst), req.semitones)
            except RuntimeError as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc
            await upload_from_path(dst, req.output_url)

    asyncio.run(_run())
    return {}
