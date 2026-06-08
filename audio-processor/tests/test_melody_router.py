from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from main import app
from routers.melody import get_melody_service

# ---------------------------------------------------------------------------
# Stub service
# ---------------------------------------------------------------------------


class _StubMelodyService:
    """Records calls and optionally raises on extract()."""

    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple[str, str]] = []
        self._raise = raise_exc

    def extract(self, vocals_path: str, output_path: str) -> None:
        self.calls.append((vocals_path, output_path))
        if self._raise is not None:
            raise self._raise


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def client_with_stub():
    """Yields (client, stub). Default stub succeeds silently."""
    stub = _StubMelodyService()
    app.dependency_overrides[get_melody_service] = lambda: stub
    try:
        yield TestClient(app), stub
    finally:
        app.dependency_overrides.clear()


@pytest.fixture
def client_with_raising_stub():
    """Factory fixture — caller passes the exception to raise."""

    def _make(exc: Exception):
        stub = _StubMelodyService(raise_exc=exc)
        app.dependency_overrides[get_melody_service] = lambda: stub
        return TestClient(app), stub

    try:
        yield _make
    finally:
        app.dependency_overrides.clear()


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_melody_happy_path(client_with_stub) -> None:
    """POST /melody with valid body → 200 with output_path."""
    client, stub = client_with_stub
    body = {"vocals_path": "/a/vocals.wav", "output_path": "/b/melody.json"}

    response = client.post("/melody", json=body)

    assert response.status_code == 200
    data = response.json()
    assert data["output_path"] == "/b/melody.json"
    assert stub.calls == [("/a/vocals.wav", "/b/melody.json")]


@pytest.mark.parametrize(
    "body",
    [
        {"output_path": "/b/melody.json"},  # missing vocals_path
        {"vocals_path": "/a/vocals.wav"},  # missing output_path
        {"vocals_path": "", "output_path": "/b/melody.json"},  # empty vocals_path
        {"vocals_path": "/a/vocals.wav", "output_path": ""},  # empty output_path
    ],
    ids=["missing-vocals", "missing-output", "empty-vocals", "empty-output"],
)
def test_melody_missing_required_fields_returns_422(body: dict, client_with_stub) -> None:
    """Missing or empty required fields → 422 (pydantic validation)."""
    client, stub = client_with_stub

    response = client.post("/melody", json=body)

    assert response.status_code == 422
    assert stub.calls == []


def test_melody_file_not_found_returns_404(client_with_raising_stub) -> None:
    """Service raises FileNotFoundError → 404 with 'vocals_path' in detail."""
    client, stub = client_with_raising_stub(FileNotFoundError("no such file"))
    body = {"vocals_path": "/missing/vocals.wav", "output_path": "/b/melody.json"}

    response = client.post("/melody", json=body)

    assert response.status_code == 404
    assert "vocals_path" in response.json()["detail"].lower()


def test_melody_runtime_error_returns_500(client_with_raising_stub) -> None:
    """Service raises RuntimeError → 500 with the error message in detail."""
    client, stub = client_with_raising_stub(RuntimeError("crepe exploded"))
    body = {"vocals_path": "/a/vocals.wav", "output_path": "/b/melody.json"}

    response = client.post("/melody", json=body)

    assert response.status_code == 500
    assert "crepe" in response.json()["detail"]
