"""Tests for GET /api/v1/versions."""

from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from fastapi.testclient import TestClient

import api.routes as api_routes
from api.app import app


@pytest.fixture(autouse=True)
def reset_versions_cache():
    api_routes._vllm_versions_cache = None
    yield
    api_routes._vllm_versions_cache = None


def make_mock_response(status_code: int, json_data: dict):
    response = MagicMock()
    response.status_code = status_code
    response.json.return_value = json_data
    return response


def test_versions_proxies_vllm_capability():
    client = TestClient(app)
    backend_response = make_mock_response(200, {
        "vllm_version": "0.0.1",
        "poc_validation_inference": True,
    })

    with patch("api.proxy.get_healthy_backends", return_value=[5001]), \
         patch("api.proxy.call_backend", new=AsyncMock(return_value=backend_response)) as call_backend:
        response = client.get("/api/v1/versions")

    assert response.status_code == 200
    data = response.json()
    assert data["version"]
    assert data["vllm_version"] == "0.0.1"
    assert data["poc_validation_inference"] is True
    call_backend.assert_awaited_once_with(5001, "GET", "/api/v1/pow/versions")


def test_versions_uses_cached_capability_after_success():
    client = TestClient(app)
    backend_response = make_mock_response(200, {
        "vllm_version": "0.0.1",
        "poc_validation_inference": True,
    })

    with patch("api.proxy.get_healthy_backends", return_value=[5001]), \
         patch("api.proxy.call_backend", new=AsyncMock(return_value=backend_response)) as call_backend:
        first_response = client.get("/api/v1/versions")
        second_response = client.get("/api/v1/versions")

    assert first_response.status_code == 200
    assert second_response.status_code == 200
    assert second_response.json()["poc_validation_inference"] is True
    call_backend.assert_awaited_once_with(5001, "GET", "/api/v1/pow/versions")


def test_versions_no_backend_fails_closed():
    client = TestClient(app)

    with patch("api.proxy.get_healthy_backends", return_value=[]), \
         patch("api.proxy.call_backend", new=AsyncMock()) as call_backend:
        response = client.get("/api/v1/versions")

    assert response.status_code == 200
    data = response.json()
    assert data["version"]
    assert data["vllm_version"] is None
    assert data["poc_validation_inference"] is False
    call_backend.assert_not_awaited()


def test_versions_backend_error_fails_closed():
    client = TestClient(app)

    with patch("api.proxy.get_healthy_backends", return_value=[5001]), \
         patch("api.proxy.call_backend", new=AsyncMock(side_effect=RuntimeError("boom"))):
        response = client.get("/api/v1/versions")

    assert response.status_code == 200
    data = response.json()
    assert data["vllm_version"] is None
    assert data["poc_validation_inference"] is False


def test_versions_backend_404_fails_closed():
    client = TestClient(app)
    backend_response = make_mock_response(404, {"detail": "Not Found"})

    with patch("api.proxy.get_healthy_backends", return_value=[5001]), \
         patch("api.proxy.call_backend", new=AsyncMock(return_value=backend_response)):
        response = client.get("/api/v1/versions")

    assert response.status_code == 200
    data = response.json()
    assert data["vllm_version"] is None
    assert data["poc_validation_inference"] is False


def test_versions_malformed_backend_response_fails_closed():
    client = TestClient(app)
    backend_response = make_mock_response(200, ["not", "an", "object"])

    with patch("api.proxy.get_healthy_backends", return_value=[5001]), \
         patch("api.proxy.call_backend", new=AsyncMock(return_value=backend_response)):
        response = client.get("/api/v1/versions")

    assert response.status_code == 200
    data = response.json()
    assert data["vllm_version"] is None
    assert data["poc_validation_inference"] is False
