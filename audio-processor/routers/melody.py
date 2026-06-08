from __future__ import annotations

from functools import lru_cache
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from services.melody_service import MelodyService


class MelodyRequest(BaseModel):
    vocals_path: str = Field(min_length=1)
    output_path: str = Field(min_length=1)


class MelodyResponse(BaseModel):
    output_path: str


@lru_cache(maxsize=1)
def get_melody_service() -> MelodyService:
    """Singleton MelodyService. Tests override via app.dependency_overrides."""
    return MelodyService()


MelodyServiceDep = Annotated[MelodyService, Depends(get_melody_service)]
router = APIRouter()


@router.post("/melody", response_model=MelodyResponse)
def melody(req: MelodyRequest, service: MelodyServiceDep) -> MelodyResponse:
    """Extract pitch timeline from a vocals stem and write melody JSON."""
    try:
        service.extract(req.vocals_path, req.output_path)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail="vocals_path not found") from exc
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc
    return MelodyResponse(output_path=req.output_path)
