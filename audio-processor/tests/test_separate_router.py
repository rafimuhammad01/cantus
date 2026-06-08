from __future__ import annotations

import os

import pytest
from fastapi.testclient import TestClient

from main import app
from routers.separate import get_demucs_service

# ---------------------------------------------------------------------------
# Stub service
# ---------------------------------------------------------------------------


class _StubDemucsService:
    """Records calls and optionally raises on separate()."""

    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str]] = []
        self._raise = raise_exc

    def separate(self, input_path: str, output_dir: str) -> None:
        self.calls.append((input_path, output_dir))
        if self._raise is not None:
            raise self._raise


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def client_with_stub():
    """Yields (client, stub). Default stub succeeds silently."""
    stub = _StubDemucsService()
    app.dependency_overrides[get_demucs_service] = lambda: stub
    try:
        yield TestClient(app), stub
    finally:
        app.dependency_overrides.clear()


@pytest.fixture
def client_with_raising_stub():
    """Factory fixture — caller passes the exception to raise."""

    def _make(exc: Exception):
        stub = _StubDemucsService(raise_exc=exc)
        app.dependency_overrides[get_demucs_service] = lambda: stub
        return TestClient(app), stub

    try:
        yield _make
    finally:
        app.dependency_overrides.clear()


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_separate_happy_path(client_with_stub) -> None:
    """POST /separate with valid body → 200 with vocals_path and no_vocals_path."""
    client, stub = client_with_stub
    body = {"input_path": "/a/track.wav", "output_dir": "/b/out"}

    response = client.post("/separate", json=body)

    assert response.status_code == 200
    data = response.json()
    assert data["vocals_path"] == os.path.join("/b/out", "vocals.wav")
    assert data["no_vocals_path"] == os.path.join("/b/out", "no_vocals.wav")
    assert stub.calls == [("/a/track.wav", "/b/out")]


@pytest.mark.parametrize(
    "body",
    [
        {"output_dir": "/b/out"},  # missing input_path
        {"input_path": "/a/track.wav"},  # missing output_dir
        {"input_path": "", "output_dir": "/b/out"},  # empty input_path
        {"input_path": "/a/track.wav", "output_dir": ""},  # empty output_dir
    ],
    ids=["missing-input", "missing-output", "empty-input", "empty-output"],
)
def test_separate_missing_required_fields_returns_422(body: dict, client_with_stub) -> None:
    """Missing or empty required fields → 422 (pydantic validation)."""
    client, stub = client_with_stub

    response = client.post("/separate", json=body)

    assert response.status_code == 422
    assert stub.calls == []


def test_separate_file_not_found_returns_404(client_with_raising_stub) -> None:
    """Service raises FileNotFoundError → 404 with 'input_path' in detail."""
    client, stub = client_with_raising_stub(FileNotFoundError("no such file"))
    body = {"input_path": "/missing/track.wav", "output_dir": "/b/out"}

    response = client.post("/separate", json=body)

    assert response.status_code == 404
    assert "input_path" in response.json()["detail"].lower()


def test_separate_runtime_error_returns_500(client_with_raising_stub) -> None:
    """Service raises RuntimeError → 500 with the error message in detail."""
    client, stub = client_with_raising_stub(RuntimeError("demucs blew up"))
    body = {"input_path": "/a/track.wav", "output_dir": "/b/out"}

    response = client.post("/separate", json=body)

    assert response.status_code == 500
    assert "demucs" in response.json()["detail"]
