from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from main import app
from routers import preview_key as pk_router
from routers.preview_key import get_preview_key_service


class _StubPreviewKeyService:
    def __init__(self, *, key: str = "A minor", raise_exc: Exception | None = None) -> None:
        self.calls: list[str] = []
        self._key = key
        self._raise = raise_exc

    def estimate(self, input_path: str) -> str:
        self.calls.append(input_path)
        if self._raise is not None:
            raise self._raise
        return self._key


@pytest.fixture
def stub_download(monkeypatch, tmp_path):
    state = {"url": None}

    async def fake_download(url: str, scratch: Path) -> Path:
        state["url"] = url
        p = scratch / "preview.bin"
        p.write_bytes(b"preview-bytes")
        return p

    monkeypatch.setattr(pk_router, "download_to_temp", fake_download)
    return state


def test_preview_key_happy_path(stub_download):
    stub = _StubPreviewKeyService(key="C major")
    app.dependency_overrides[get_preview_key_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post("/preview-key", json={"input_url": "https://r2.test/p.mp3"})
        assert resp.status_code == 200
        assert resp.json() == {"key": "C major"}
        assert stub_download["url"] == "https://r2.test/p.mp3"
        assert len(stub.calls) == 1
    finally:
        app.dependency_overrides.clear()


@pytest.mark.parametrize(
    "body,ids",
    [
        ({}, "missing"),
        ({"input_url": ""}, "empty"),
    ],
)
def test_preview_key_invalid_body_returns_422(body, ids, stub_download):
    client = TestClient(app)
    resp = client.post("/preview-key", json=body)
    assert resp.status_code == 422


def test_preview_key_service_error_returns_500(stub_download):
    stub = _StubPreviewKeyService(raise_exc=RuntimeError("librosa boom"))
    app.dependency_overrides[get_preview_key_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post("/preview-key", json={"input_url": "u"})
        assert resp.status_code == 500
        assert "librosa" in resp.json()["detail"]
    finally:
        app.dependency_overrides.clear()
