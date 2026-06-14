from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from main import app
from routers import melody as melody_router
from routers.melody import get_melody_service


class _StubMelodyService:
    """Records calls and optionally raises on extract()."""

    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str]] = []
        self._raise = raise_exc

    def extract(self, vocals_path: str, output_path: str) -> None:
        self.calls.append((vocals_path, output_path))
        if self._raise is not None:
            raise self._raise
        # Write output bytes so upload_from_path sees content.
        Path(output_path).write_bytes(b'{"key":"A","series":[]}')


@pytest.fixture
def stub_io(monkeypatch, tmp_path):
    """Stubs download_to_temp and upload_from_path on the melody router.
    Returns a dict tracking calls."""
    state = {
        "input_url": None,
        "output_url": None,
        "uploaded": None,
    }

    async def fake_download(url: str, scratch: Path) -> Path:
        state["input_url"] = url
        p = scratch / "in.bin"
        p.write_bytes(b"vocals-bytes")
        return p

    async def fake_upload(path: Path, url: str) -> None:
        state["output_url"] = url
        state["uploaded"] = path.read_bytes() if path.exists() else None

    monkeypatch.setattr(melody_router, "download_to_temp", fake_download)
    monkeypatch.setattr(melody_router, "upload_from_path", fake_upload)
    return state


@pytest.fixture
def client_with_stub(stub_io):
    stub = _StubMelodyService()
    app.dependency_overrides[get_melody_service] = lambda: stub
    try:
        yield TestClient(app), stub, stub_io
    finally:
        app.dependency_overrides.clear()


def test_melody_happy_path(client_with_stub) -> None:
    """POST /melody with valid body → 200 {}; assert upload received the JSON bytes."""
    client, stub, io_state = client_with_stub
    body = {
        "vocals_input_url": "https://r2.test/vocals.wav",
        "output_url": "https://r2.test/melody.json",
    }

    resp = client.post("/melody", json=body)

    assert resp.status_code == 200
    assert resp.json() == {}
    assert io_state["input_url"] == "https://r2.test/vocals.wav"
    assert io_state["output_url"] == "https://r2.test/melody.json"
    assert io_state["uploaded"] == b'{"key":"A","series":[]}'
    assert len(stub.calls) == 1


@pytest.mark.parametrize(
    "body",
    [
        {"output_url": "/b/melody.json"},  # missing vocals_input_url
        {"vocals_input_url": "/a/vocals.wav"},  # missing output_url
        {"vocals_input_url": "", "output_url": "/b/melody.json"},  # empty vocals_input_url
        {"vocals_input_url": "/a/vocals.wav", "output_url": ""},  # empty output_url
    ],
    ids=[
        "missing-vocals-input-url",
        "missing-output-url",
        "empty-vocals-input-url",
        "empty-output-url",
    ],
)
def test_melody_missing_required_fields_returns_422(body: dict, client_with_stub) -> None:
    """Missing or empty required fields → 422 (pydantic validation)."""
    client, stub, _ = client_with_stub

    resp = client.post("/melody", json=body)

    assert resp.status_code == 422
    assert stub.calls == []


def test_melody_runtime_error_returns_500(monkeypatch, stub_io) -> None:
    """Service raises RuntimeError → 500 with the error message in detail."""
    stub = _StubMelodyService(raise_exc=RuntimeError("crepe exploded"))
    app.dependency_overrides[get_melody_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post(
            "/melody",
            json={
                "vocals_input_url": "https://r2.test/vocals.wav",
                "output_url": "https://r2.test/melody.json",
            },
        )
        assert resp.status_code == 500
        assert "crepe" in resp.json()["detail"]
    finally:
        app.dependency_overrides.clear()
