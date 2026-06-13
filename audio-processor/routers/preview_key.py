from __future__ import annotations

import asyncio
import tempfile
from functools import lru_cache
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field

from routers._io_url import download_to_temp
from services.preview_key_service import PreviewKeyService


class PreviewKeyRequest(BaseModel):
    input_url: str = Field(min_length=1)


class PreviewKeyResponse(BaseModel):
    key: str


@lru_cache(maxsize=1)
def get_preview_key_service() -> PreviewKeyService:
    """Singleton PreviewKeyService. Tests override via app.dependency_overrides."""
    return PreviewKeyService()


PreviewKeyServiceDep = Annotated[PreviewKeyService, Depends(get_preview_key_service)]
router = APIRouter()


@router.post("/preview-key", response_model=PreviewKeyResponse)
def preview_key(req: PreviewKeyRequest, service: PreviewKeyServiceDep) -> PreviewKeyResponse:
    """Download input_url → estimate musical key."""

    async def _run() -> str:
        with tempfile.TemporaryDirectory(prefix="preview-key-") as td:
            src = await download_to_temp(req.input_url, Path(td))
            try:
                return service.estimate(str(src))
            except Exception as exc:
                raise HTTPException(status_code=500, detail=str(exc)) from exc

    key = asyncio.run(_run())
    return PreviewKeyResponse(key=key)
