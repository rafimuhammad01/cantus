from __future__ import annotations

import asyncio
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp, upload_from_path
from services.melody_service import MelodyService


class MelodyRequest(BaseModel):
    vocals_input_url: str = Field(min_length=1)
    output_url: str = Field(min_length=1)


@lru_cache(maxsize=1)
def get_melody_service() -> MelodyService:
    """Singleton MelodyService. Tests override via app.dependency_overrides."""
    return MelodyService()


MelodyServiceDep = Annotated[MelodyService, Depends(get_melody_service)]
router = APIRouter()


@router.post("/melody")
def melody(req: MelodyRequest, service: MelodyServiceDep) -> dict:
    """Download vocals stem → extract melody → upload melody.json."""

    async def _run() -> None:
        with tempfile.TemporaryDirectory(prefix="melody-") as td:
            scratch = Path(td)
            src = await download_to_temp(req.vocals_input_url, scratch)
            dst = scratch / "melody.json"
            try:
                service.extract(str(src), str(dst))
            except RuntimeError as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc
            await upload_from_path(dst, req.output_url)

    asyncio.run(_run())
    return {}
