import os

from fastapi import FastAPI

from logging_config import setup_logging
from routers import melody as melody_router
from routers import preview_key as preview_key_router
from routers import separate as separate_router

setup_logging(os.environ.get("LOG_LEVEL", "info"))

app = FastAPI(title="cantus audio-processor")
app.include_router(separate_router.router)
app.include_router(melody_router.router)
app.include_router(preview_key_router.router)


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}
