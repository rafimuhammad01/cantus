from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from main import app
from routers import separate as sep_router
from routers.separate import get_roformer_service


class _StubRoformer:
    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str]] = []
        self._raise = raise_exc

    def separate(self, input_path: str, output_dir: str) -> None:
        self.calls.append((input_path, output_dir))
        if self._raise is not None:
            raise self._raise
        # Simulate Roformer writing both stems into output_dir.
        Path(output_dir, "vocals.mp3").write_bytes(b"VOCALS")
        Path(output_dir, "no_vocals.mp3").write_bytes(b"NO-VOCALS")


@pytest.fixture
def stub_io(monkeypatch):
    state = {
        "input_url": None,
        "vocals_url": None,
        "no_vocals_url": None,
        "uploaded": {},
    }

    async def fake_download(url: str, scratch: Path) -> Path:
        state["input_url"] = url
        p = scratch / "in.mp3"
        p.write_bytes(b"input")
        return p

    async def fake_upload(path: Path, url: str) -> None:
        state["uploaded"][url] = path.read_bytes()

    monkeypatch.setattr(sep_router, "download_to_temp", fake_download)
    monkeypatch.setattr(sep_router, "upload_from_path", fake_upload)
    return state


def test_separate_happy_path(stub_io):
    stub = _StubRoformer()
    app.dependency_overrides[get_roformer_service] = lambda: stub
    stub_io["vocals_url"] = "https://r2.test/v.wav"
    stub_io["no_vocals_url"] = "https://r2.test/nv.wav"
    try:
        client = TestClient(app)
        resp = client.post(
            "/separate",
            json={
                "input_url": "https://r2.test/in.mp3",
                "vocals_output_url": stub_io["vocals_url"],
                "no_vocals_output_url": stub_io["no_vocals_url"],
            },
        )
        assert resp.status_code == 204
        assert stub_io["input_url"] == "https://r2.test/in.mp3"
        assert stub_io["uploaded"][stub_io["vocals_url"]] == b"VOCALS"
        assert stub_io["uploaded"][stub_io["no_vocals_url"]] == b"NO-VOCALS"
        assert len(stub.calls) == 1
    finally:
        app.dependency_overrides.clear()


@pytest.mark.parametrize(
    "body",
    [
        {"vocals_output_url": "v", "no_vocals_output_url": "nv"},
        {"input_url": "i", "no_vocals_output_url": "nv"},
        {"input_url": "i", "vocals_output_url": "v"},
        {"input_url": "", "vocals_output_url": "v", "no_vocals_output_url": "nv"},
    ],
    ids=["missing-input", "missing-vocals", "missing-novocals", "empty-input"],
)
def test_separate_missing_required_fields_returns_422(body, stub_io):
    client = TestClient(app)
    resp = client.post("/separate", json=body)
    assert resp.status_code == 422


def test_separate_runtime_error_returns_500(stub_io):
    stub = _StubRoformer(raise_exc=RuntimeError("roformer OOM"))
    app.dependency_overrides[get_roformer_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post(
            "/separate",
            json={
                "input_url": "i",
                "vocals_output_url": "v",
                "no_vocals_output_url": "nv",
            },
        )
        assert resp.status_code == 500
        assert "roformer" in resp.json()["detail"].lower()
    finally:
        app.dependency_overrides.clear()
