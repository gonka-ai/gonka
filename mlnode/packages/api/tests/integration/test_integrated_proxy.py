import pytest
import asyncio
import requests
from unittest.mock import patch, MagicMock
from fastapi.testclient import TestClient

from api.app import app


@pytest.fixture
def client():
    return TestClient(app)


def test_api_endpoints_accessible(client):
    """Test that /api endpoints are still accessible."""
    # Test that the API is running
    response = client.get("/docs")
    assert response.status_code == 200


def test_v1_endpoints_proxy_when_no_backend(client):
    """Test that /v1 endpoints return 503 when no vLLM backend is available."""
    response = client.get("/v1/models")
    assert response.status_code == 503
    assert b"No vLLM backend available" in response.content


@patch('api.proxy.vllm_backend_ports', [5001, 5002])
@patch('api.proxy.vllm_healthy', {5001: True, 5002: True})
@patch('api.proxy.vllm_counts', {5001: 0, 5002: 0})
def test_v1_endpoints_proxy_with_backend(client):
    """Test that /v1 endpoints are properly routed when backends are available."""
    # Mock the httpx client to simulate backend response
    with patch('api.proxy.vllm_client') as mock_client:
        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.headers = {"content-type": "application/json"}
        mock_response.aiter_raw.return_value = iter([b'{"models": []}'])
        
        mock_stream = MagicMock()
        mock_stream.__aenter__.return_value = mock_response
        mock_client.stream.return_value = mock_stream
        
        response = client.get("/v1/models")
        # Should be routed to backend (mocked)
        assert mock_client.stream.called


def test_proxy_middleware_order():
    """Test that proxy middleware is properly configured."""
    # Check that ProxyMiddleware is in the middleware stack
    middleware_found = False
    for middleware in app.user_middleware:
        if "ProxyMiddleware" in str(middleware.cls):
            middleware_found = True
            break
    
    assert middleware_found, "ProxyMiddleware should be in the middleware stack"


@pytest.mark.asyncio
async def test_proxy_lifecycle():
    """Test proxy startup and shutdown lifecycle."""
    from api.proxy import start_vllm_proxy, stop_vllm_proxy
    
    # Test startup
    await start_vllm_proxy()
    
    # Import after startup to get the updated value
    from api.proxy import vllm_client
    assert vllm_client is not None
    
    # Test shutdown
    await stop_vllm_proxy()
    
    # Import after shutdown to get the updated value
    from api.proxy import vllm_client
    assert vllm_client is None 