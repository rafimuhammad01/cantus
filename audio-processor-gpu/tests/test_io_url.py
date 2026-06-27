from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Any

import httpx
import pytest
from fastapi import HTTPException

from routers._io_url import download_to_temp, upload_from_path


def _run(coro):
    return asyncio.run(coro)


class _FakeStreamResponse:
    def __init__(self, status_code: int, chunks: list[bytes]) -> None:
        self.status_code = status_code
        self._chunks = chunks

    async def aiter_bytes(self):
        for c in self._chunks:
            yield c

    def raise_for_status(self) -> None:
        if self.status_code >= 400:
            raise httpx.HTTPStatusError(
                "boom",
                request=httpx.Request("GET", "http://test"),
                response=httpx.Response(self.status_code),
            )


class _FakeClient:
    """Minimal stand-in for httpx.AsyncClient — supports stream() context manager
    and put(), records last call for assertions."""

    def __init__(
        self,
        *,
        get_response: _FakeStreamResponse | None = None,
        put_status: int = 200,
        raise_on: str | None = None,  # "get" or "put"
    ) -> None:
        self.get_response = get_response or _FakeStreamResponse(200, [b"hello"])
        self.put_status = put_status
        self.raise_on = raise_on
        self.last_put_body: bytes | None = None
        self.last_put_headers: dict = {}

    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc) -> None:
        return None

    def stream(self, method: str, url: str):
        outer = self

        class _Ctx:
            async def __aenter__(self_inner):
                if outer.raise_on == "get":
                    raise httpx.ConnectError("network down")
                return outer.get_response

            async def __aexit__(self_inner, *exc):
                return None

        return _Ctx()

    async def put(self, url: str, content: Any, headers: dict | None = None) -> httpx.Response:
        if self.raise_on == "put":
            raise httpx.ConnectError("network down")
        # `content` is an async iterator of bytes for streaming PUT.
        body = b""
        async for chunk in content:
            body += chunk
        self.last_put_body = body
        self.last_put_headers = dict(headers or {})
        return httpx.Response(self.put_status)


@pytest.fixture
def scratch(tmp_path: Path) -> Path:
    return tmp_path


def test_download_to_temp_writes_streamed_bytes(scratch, monkeypatch):
    fake = _FakeClient(get_response=_FakeStreamResponse(200, [b"abc", b"def"]))
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    out = _run(download_to_temp("http://test/file", scratch))

    assert out.parent == scratch
    assert out.read_bytes() == b"abcdef"


def test_download_to_temp_http_error_raises_502(scratch, monkeypatch):
    fake = _FakeClient(get_response=_FakeStreamResponse(404, []))
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(download_to_temp("http://test/missing", scratch))
    assert ei.value.status_code == 502


def test_download_to_temp_network_error_raises_502(scratch, monkeypatch):
    fake = _FakeClient(raise_on="get")
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(download_to_temp("http://test/file", scratch))
    assert ei.value.status_code == 502


def test_upload_from_path_streams_file(scratch, monkeypatch):
    src = scratch / "out.bin"
    src.write_bytes(b"payload")
    fake = _FakeClient(put_status=200)
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    _run(upload_from_path(src, "http://test/dest"))

    assert fake.last_put_body == b"payload"


def test_upload_from_path_http_error_raises_502(scratch, monkeypatch):
    src = scratch / "out.bin"
    src.write_bytes(b"payload")
    fake = _FakeClient(put_status=500)
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(upload_from_path(src, "http://test/dest"))
    assert ei.value.status_code == 502


def test_upload_from_path_network_error_raises_502(scratch, monkeypatch):
    src = scratch / "out.bin"
    src.write_bytes(b"payload")
    fake = _FakeClient(raise_on="put")
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    with pytest.raises(HTTPException) as ei:
        _run(upload_from_path(src, "http://test/dest"))
    assert ei.value.status_code == 502


@pytest.mark.parametrize(
    "filename, want_ct",
    [
        ("audio.mp3", "audio/mpeg"),
        ("melody.json", "application/json"),
        ("file.cantusnoext", None),  # unknown extension → header must be absent
    ],
)
def test_upload_from_path_sets_content_type(scratch, monkeypatch, filename, want_ct):
    src = scratch / filename
    src.write_bytes(b"data")
    fake = _FakeClient(put_status=200)
    monkeypatch.setattr("routers._io_url._client", lambda: fake)

    _run(upload_from_path(src, "http://test/dest"))

    if want_ct is None:
        assert "Content-Type" not in fake.last_put_headers
    else:
        assert fake.last_put_headers.get("Content-Type") == want_ct
