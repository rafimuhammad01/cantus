from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from main import app
from routers.shift import get_pitch_service

# ---------------------------------------------------------------------------
# Stub service
# ---------------------------------------------------------------------------


class _StubPitchService:
    """Records calls and optionally raises on shift()."""

    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str, float]] = []
        self._raise = raise_exc

    def shift(self, input_path: str, output_path: str, semitones: float) -> None:
        self.calls.append((input_path, output_path, semitones))
        if self._raise is not None:
            raise self._raise


# ---------------------------------------------------------------------------
# Fixture
# ---------------------------------------------------------------------------


@pytest.fixture
def client_with_stub():
    """Yields (client, stub). Default stub succeeds silently."""
    stub = _StubPitchService()
    app.dependency_overrides[get_pitch_service] = lambda: stub
    try:
        yield TestClient(app), stub
    finally:
        app.dependency_overrides.clear()


@pytest.fixture
def client_with_raising_stub():
    """Factory fixture — caller passes the exception to raise."""

    def _make(exc: Exception):
        stub = _StubPitchService(raise_exc=exc)
        app.dependency_overrides[get_pitch_service] = lambda: stub
        client = TestClient(app)
        return client, stub

    try:
        yield _make
    finally:
        app.dependency_overrides.clear()


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_shift_happy_path(client_with_stub) -> None:
    """POST /shift with valid body → 200 and {"output_path": ...}."""
    client, stub = client_with_stub
    body = {
        "input_path": "/tmp/input.wav",
        "output_path": "/tmp/output.mp3",
        "semitones": 2.0,
    }

    response = client.post("/shift", json=body)

    assert response.status_code == 200
    assert response.json() == {"output_path": "/tmp/output.mp3"}
    assert stub.calls == [("/tmp/input.wav", "/tmp/output.mp3", 2.0)]


@pytest.mark.parametrize(
    "semitones",
    [13.0, -13.0],
    ids=["above-max", "below-min"],
)
def test_shift_semitones_out_of_range_returns_422(semitones: float, client_with_stub) -> None:
    """semitones outside [-12, 12] → 422 (pydantic validation)."""
    client, stub = client_with_stub
    body = {
        "input_path": "/tmp/input.wav",
        "output_path": "/tmp/output.mp3",
        "semitones": semitones,
    }

    response = client.post("/shift", json=body)

    assert response.status_code == 422
    assert stub.calls == []


@pytest.mark.parametrize(
    "body",
    [
        {"output_path": "/tmp/output.mp3", "semitones": 2.0},
        {"input_path": "/tmp/input.wav", "semitones": 2.0},
        {"input_path": "", "output_path": "/tmp/output.mp3", "semitones": 2.0},
        {"input_path": "/tmp/input.wav", "output_path": "", "semitones": 2.0},
    ],
    ids=["missing-input-path", "missing-output-path", "empty-input-path", "empty-output-path"],
)
def test_shift_missing_required_fields_returns_422(body: dict, client_with_stub) -> None:
    """Missing or empty required fields → 422."""
    client, stub = client_with_stub

    response = client.post("/shift", json=body)

    assert response.status_code == 422
    assert stub.calls == []


def test_shift_service_file_not_found_returns_404(client_with_raising_stub) -> None:
    """Service raises FileNotFoundError → 404."""
    client, stub = client_with_raising_stub(FileNotFoundError("no such file"))
    body = {
        "input_path": "/tmp/missing.wav",
        "output_path": "/tmp/output.mp3",
        "semitones": 0.0,
    }

    response = client.post("/shift", json=body)

    assert response.status_code == 404
    assert "not found" in response.json()["detail"].lower()


def test_shift_service_runtime_error_returns_500(client_with_raising_stub) -> None:
    """Service raises RuntimeError → 500."""
    client, stub = client_with_raising_stub(RuntimeError("ffmpeg failed: boom"))
    body = {
        "input_path": "/tmp/input.wav",
        "output_path": "/tmp/output.mp3",
        "semitones": 0.0,
    }

    response = client.post("/shift", json=body)

    assert response.status_code == 500
    assert "ffmpeg" in response.json()["detail"]
