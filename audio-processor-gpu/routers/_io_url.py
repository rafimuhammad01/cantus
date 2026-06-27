"""HTTP-streamed input download and output upload helpers used by all
processor routers. Failures surface as HTTPException(502) so the Go caller
sees a Bad Gateway and treats the job as failed without retries."""

from __future__ import annotations

import mimetypes
import uuid
from pathlib import Path

import httpx
from fastapi import HTTPException

# 60s covers a worst-case slow PUT of a ~30MB stem on a flaky connection.
# Demucs/CREPE wall-clock is bounded separately by the GPU 180s timeout.
_HTTP_TIMEOUT = httpx.Timeout(60.0)


def _client() -> httpx.AsyncClient:
    """Indirection so tests can monkeypatch a fake client."""
    return httpx.AsyncClient(timeout=_HTTP_TIMEOUT)


async def download_to_temp(url: str, scratch: Path) -> Path:
    """Stream-GET `url` into a freshly-named file under `scratch`. Returns the
    written path. Raises HTTPException(502) on any HTTP or network error."""
    dst = scratch / f"in-{uuid.uuid4().hex}.bin"
    try:
        async with _client() as client:
            async with client.stream("GET", url) as resp:
                resp.raise_for_status()
                with dst.open("wb") as f:
                    async for chunk in resp.aiter_bytes():
                        f.write(chunk)
    except (httpx.HTTPError, httpx.HTTPStatusError) as exc:
        raise HTTPException(status_code=502, detail=f"download failed: {exc}") from exc
    return dst


async def upload_from_path(path: Path, url: str) -> None:
    """Stream-PUT `path` to `url`. Raises HTTPException(502) on failure.

    Cloudflare R2 rejects chunked-transfer PUTs with 411 Length Required, so we
    pre-compute Content-Length from the file stat and pass it explicitly. httpx
    skips chunked encoding when Content-Length is set on the request.
    """

    size = path.stat().st_size
    headers = {"Content-Length": str(size)}
    ct, _ = mimetypes.guess_type(str(path))
    if ct:
        headers["Content-Type"] = ct

    async def _iter_file():
        with path.open("rb") as f:
            while True:
                chunk = f.read(64 * 1024)
                if not chunk:
                    break
                yield chunk

    try:
        async with _client() as client:
            resp = await client.put(url, content=_iter_file(), headers=headers)
            if resp.status_code >= 400:
                raise HTTPException(
                    status_code=502,
                    detail=f"upload failed: status {resp.status_code}",
                )
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"upload failed: {exc}") from exc
