import os

from fastapi import FastAPI

from logging_config import setup_logging
from routers import melody as melody_router
from routers import search as search_router
from routers import separate as separate_router
from routers import shift as shift_router

setup_logging(os.environ.get("LOG_LEVEL", "info"))

app = FastAPI(title="cantus audio-processor")
app.include_router(search_router.router)
app.include_router(shift_router.router)
app.include_router(separate_router.router)
app.include_router(melody_router.router)


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}
