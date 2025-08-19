import pytest
import asyncio
from unittest.mock import AsyncMock, MagicMock, patch
from fastapi import Request

from api.proxy import (
    start_backward_compatibility, 
    stop_backward_compatibility,
    _compatibility_proxy_handler
)


@pytest.mark.asyncio
async def test_backward_compatibility_start_stop():
    """Test that backward compatibility server can start and stop."""
    
    # Test start
    await start_backward_compatibility()
    
    # Test stop
    await stop_backward_compatibility()


@pytest.mark.asyncio
async def test_compatibility_proxy_handler_no_backends():
    """Test compatibility proxy handler when no backends are available."""
    from api.proxy import vllm_backend_ports
    
    # Mock request
    mock_request = MagicMock(spec=Request)
    mock_request.url.path = "/v1/models"
    mock_request.method = "GET"
    mock_request.headers = {}
    mock_request.query_params = {}
    mock_request.stream.return_value = []
    
    # Clear backends
    original_backends = vllm_backend_ports.copy()
    vllm_backend_ports.clear()
    
    try:
        response = await _compatibility_proxy_handler(mock_request, "v1/models")
        assert response.status_code == 503
        assert b"No vLLM backend available" in response.body
    finally:
        # Restore backends
        vllm_backend_ports.extend(original_backends)


@pytest.mark.asyncio
async def test_compatibility_proxy_handler_with_backends():
    """Test compatibility proxy handler when backends are available."""
    from api.proxy import vllm_backend_ports, vllm_healthy, vllm_counts, vllm_client
    
    # Mock request
    mock_request = MagicMock(spec=Request)
    mock_request.url.path = "/v1/models"
    mock_request.method = "GET"
    mock_request.headers = {}
    mock_request.query_params = {}
    mock_request.stream.return_value = []
    
    # Setup mock backends
    original_backends = vllm_backend_ports.copy()
    original_healthy = vllm_healthy.copy()
    original_counts = vllm_counts.copy()
    original_client = vllm_client
    
    vllm_backend_ports.clear()
    vllm_backend_ports.extend([5001, 5002])
    vllm_healthy.update({5001: True, 5002: True})
    vllm_counts.update({5001: 0, 5002: 0})
    
    # Mock httpx client
    mock_response = MagicMock()
    mock_response.status_code = 200
    mock_response.headers = {"content-type": "application/json"}
    mock_response.aiter_raw.return_value = iter([b'{"models": []}'])
    
    mock_stream = MagicMock()
    mock_stream.__aenter__.return_value = mock_response
    
    with patch('api.proxy.vllm_client') as mock_client:
        mock_client.stream.return_value = mock_stream
        
        response = await _compatibility_proxy_handler(mock_request, "v1/models")
        assert response.status_code == 200
    
    # Restore original state
    vllm_backend_ports.clear()
    vllm_backend_ports.extend(original_backends)
    vllm_healthy.clear()
    vllm_healthy.update(original_healthy)
    vllm_counts.clear()
    vllm_counts.update(original_counts) 