from __future__ import annotations

from functools import lru_cache
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from services.pitch_service import PitchService


class ShiftRequest(BaseModel):
    input_path: str = Field(min_length=1)
    output_path: str = Field(min_length=1)
    semitones: float = Field(ge=-12, le=12)


@lru_cache(maxsize=1)
def get_pitch_service() -> PitchService:
    """Singleton PitchService. Tests override via app.dependency_overrides."""
    return PitchService()


ShiftServiceDep = Annotated[PitchService, Depends(get_pitch_service)]

router = APIRouter()


@router.post("/shift")
def shift(req: ShiftRequest, service: ShiftServiceDep) -> dict[str, str]:
    try:
        service.shift(req.input_path, req.output_path, req.semitones)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail="input_path not found") from exc
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc
    return {"output_path": req.output_path}
