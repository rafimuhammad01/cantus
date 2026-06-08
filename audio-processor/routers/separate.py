from __future__ import annotations

import os
from functools import lru_cache
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from services.demucs_service import DemucsService


class SeparateRequest(BaseModel):
    input_path: str = Field(min_length=1)
    output_dir: str = Field(min_length=1)


class SeparateResponse(BaseModel):
    vocals_path: str
    no_vocals_path: str


@lru_cache(maxsize=1)
def get_demucs_service() -> DemucsService:
    """Singleton DemucsService. Tests override via app.dependency_overrides."""
    return DemucsService(device=os.environ.get("DEVICE", "cpu"))


SeparateServiceDep = Annotated[DemucsService, Depends(get_demucs_service)]
router = APIRouter()


@router.post("/separate", response_model=SeparateResponse)
def separate(req: SeparateRequest, service: SeparateServiceDep) -> SeparateResponse:
    """Separate audio into vocals and no_vocals stems."""
    try:
        service.separate(req.input_path, req.output_dir)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail="input_path not found") from exc
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc
    return SeparateResponse(
        vocals_path=os.path.join(req.output_dir, "vocals.wav"),
        no_vocals_path=os.path.join(req.output_dir, "no_vocals.wav"),
    )
