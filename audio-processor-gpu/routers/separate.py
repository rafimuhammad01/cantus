from __future__ import annotations

import asyncio
import os
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Response
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp, upload_from_path
from services.roformer_service import RoformerService


class SeparateRequest(BaseModel):
    input_url: str = Field(min_length=1)
    vocals_output_url: str = Field(min_length=1)
    no_vocals_output_url: str = Field(min_length=1)


@lru_cache(maxsize=1)
def get_roformer_service() -> RoformerService:
    return RoformerService(
        model_dir=os.environ.get("MODEL_DIR", "./tmp/models"),
        model_filename=os.environ.get(
            "ROFORMER_MODEL_FILENAME",
            "model_bs_roformer_ep_317_sdr_12.9755.ckpt",
        ),
    )


SeparateServiceDep = Annotated[RoformerService, Depends(get_roformer_service)]
router = APIRouter()


@router.post("/separate", status_code=204)
def separate(req: SeparateRequest, service: SeparateServiceDep) -> Response:
    """Download input → run Roformer → upload both stems."""

    async def _run() -> None:
        with tempfile.TemporaryDirectory(prefix="separate-") as td:
            scratch = Path(td)
            src = await download_to_temp(req.input_url, scratch)
            stems_dir = scratch / "stems"
            stems_dir.mkdir()
            try:
                service.separate(str(src), str(stems_dir))
            except RuntimeError as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc
            vocals = stems_dir / "vocals.wav"
            no_vocals = stems_dir / "no_vocals.wav"
            if not vocals.exists() or not no_vocals.exists():
                raise HTTPException(status_code=500, detail="roformer did not produce both stems")
            await upload_from_path(vocals, req.vocals_output_url)
            await upload_from_path(no_vocals, req.no_vocals_output_url)

    asyncio.run(_run())
    return Response(status_code=204)
