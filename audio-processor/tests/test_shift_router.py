from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from main import app
from routers import shift as shift_router
from routers.shift import get_pitch_service


class _StubPitchService:
    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str, float]] = []
        self._raise = raise_exc

    def shift(self, input_path: str, output_path: str, semitones: float) -> None:
        self.calls.append((input_path, output_path, semitones))
        if self._raise is not None:
            raise self._raise


@pytest.fixture
def stub_io(monkeypatch, tmp_path):
    """Stubs download_to_temp and upload_from_path on the shift router.
    Returns a dict tracking calls."""
    state = {"downloaded": None, "uploaded": None, "input_url": None, "output_url": None}

    async def fake_download(url: str, scratch: Path) -> Path:
        state["input_url"] = url
        p = scratch / "in.bin"
        p.write_bytes(b"audio-bytes")
        state["downloaded"] = p
        return p

    async def fake_upload(path: Path, url: str) -> None:
        state["output_url"] = url
        state["uploaded"] = path.read_bytes() if path.exists() else None

    monkeypatch.setattr(shift_router, "download_to_temp", fake_download)
    monkeypatch.setattr(shift_router, "upload_from_path", fake_upload)
    return state


@pytest.fixture
def client_with_stub(stub_io):
    stub = _StubPitchService()
    # Write a file to the output path the service "creates" so upload_from_path sees bytes.
    real_shift = stub.shift

    def shift_writing_output(input_path: str, output_path: str, semitones: float):
        real_shift(input_path, output_path, semitones)
        Path(output_path).write_bytes(b"shifted-bytes")

    stub.shift = shift_writing_output  # type: ignore[assignment]
    app.dependency_overrides[get_pitch_service] = lambda: stub
    try:
        yield TestClient(app), stub, stub_io
    finally:
        app.dependency_overrides.clear()


def test_shift_happy_path(client_with_stub):
    client, stub, io_state = client_with_stub
    body = {
        "input_url": "https://r2.test/in.wav",
        "output_url": "https://r2.test/out.mp3",
        "semitones": 2.0,
    }
    resp = client.post("/shift", json=body)
    assert resp.status_code == 200
    assert io_state["input_url"] == "https://r2.test/in.wav"
    assert io_state["output_url"] == "https://r2.test/out.mp3"
    assert io_state["uploaded"] == b"shifted-bytes"
    assert len(stub.calls) == 1
    assert stub.calls[0][2] == 2.0


@pytest.mark.parametrize("semitones", [13.0, -13.0])
def test_shift_semitones_out_of_range_returns_422(semitones, client_with_stub):
    client, stub, _ = client_with_stub
    resp = client.post(
        "/shift",
        json={
            "input_url": "https://r2.test/in",
            "output_url": "https://r2.test/out",
            "semitones": semitones,
        },
    )
    assert resp.status_code == 422
    assert stub.calls == []


@pytest.mark.parametrize(
    "body",
    [
        {"output_url": "u", "semitones": 0.0},
        {"input_url": "u", "semitones": 0.0},
        {"input_url": "", "output_url": "u", "semitones": 0.0},
        {"input_url": "u", "output_url": "", "semitones": 0.0},
    ],
    ids=["missing-input-url", "missing-output-url", "empty-input-url", "empty-output-url"],
)
def test_shift_missing_required_fields_returns_422(body, client_with_stub):
    client, stub, _ = client_with_stub
    resp = client.post("/shift", json=body)
    assert resp.status_code == 422
    assert stub.calls == []


def test_shift_service_runtime_error_returns_500(monkeypatch, stub_io):
    stub = _StubPitchService(raise_exc=RuntimeError("ffmpeg failed"))
    app.dependency_overrides[get_pitch_service] = lambda: stub
    try:
        client = TestClient(app)
        resp = client.post(
            "/shift",
            json={
                "input_url": "u",
                "output_url": "v",
                "semitones": 0.0,
            },
        )
        assert resp.status_code == 500
        assert "ffmpeg" in resp.json()["detail"]
    finally:
        app.dependency_overrides.clear()
