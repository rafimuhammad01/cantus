import pytest
from fastapi.testclient import TestClient

from main import app

client = TestClient(app)


@pytest.mark.parametrize(
    "method,path,want_status,want_body",
    [
        ("GET", "/health", 200, {"status": "ok"}),
    ],
    ids=["health-ok"],
)
def test_health_endpoint(method, path, want_status, want_body):
    response = client.request(method, path)
    assert response.status_code == want_status
    assert response.json() == want_body
