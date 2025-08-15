import pytest
import asyncio
from unittest.mock import AsyncMock, MagicMock, patch
from fastapi import Request
from fastapi.testclient import TestClient

from api.proxy import ProxyMiddleware, setup_vllm_proxy, start_vllm_proxy, stop_vllm_proxy


@pytest.fixture
def proxy_middleware():
    # Create a mock app for the BaseHTTPMiddleware constructor
    mock_app = MagicMock()
    return ProxyMiddleware(mock_app)


@pytest.fixture
def mock_request():
    request = MagicMock(spec=Request)
    request.url.path = "/v1/models"
    request.method = "GET"
    request.headers = {}
    request.query_params = {}
    request.stream.return_value = []
    return request


@pytest.mark.asyncio
async def test_proxy_middleware_routes_v1_to_vllm(proxy_middleware, mock_request):
    """Test that /v1 requests are routed to vLLM backend."""
    
    # Mock the proxy method on the middleware instance
    with patch.object(proxy_middleware, '_proxy_to_vllm') as mock_proxy:
        mock_proxy.return_value = MagicMock()
        
        # Mock call_next
        call_next = AsyncMock()
        
        # Test /v1 routing
        mock_request.url.path = "/v1/models"
        result = await proxy_middleware.dispatch(mock_request, call_next)
        
        # Should call proxy, not call_next
        mock_proxy.assert_called_once_with(mock_request)
        call_next.assert_not_called()


@pytest.mark.asyncio
async def test_proxy_middleware_routes_api_to_main(proxy_middleware, mock_request):
    """Test that /api requests are routed to main API."""
    
    # Mock call_next
    call_next = AsyncMock()
    call_next.return_value = MagicMock()
    
    # Test /api routing
    mock_request.url.path = "/api/v1/inference"
    result = await proxy_middleware.dispatch(mock_request, call_next)
    
    # Should call call_next, not proxy
    call_next.assert_called_once_with(mock_request)


@pytest.mark.asyncio
async def test_proxy_middleware_default_routing(proxy_middleware, mock_request):
    """Test that other requests default to main API."""
    
    # Mock call_next
    call_next = AsyncMock()
    call_next.return_value = MagicMock()
    
    # Test default routing
    mock_request.url.path = "/health"
    result = await proxy_middleware.dispatch(mock_request, call_next)
    
    # Should call call_next
    call_next.assert_called_once_with(mock_request)


@pytest.mark.asyncio
async def test_proxy_returns_503_when_backends_not_healthy(proxy_middleware, mock_request):
    """Test that proxy returns 503 when no backends are healthy."""
    from api.proxy import vllm_backend_ports, vllm_healthy
    
    # Setup backends but mark them as unhealthy
    original_backends = vllm_backend_ports.copy()
    original_healthy = vllm_healthy.copy()
    
    vllm_backend_ports.clear()
    vllm_backend_ports.extend([5001, 5002])
    vllm_healthy.update({5001: False, 5002: False})
    
    try:
        # Mock call_next
        call_next = AsyncMock()
        
        # Test /v1 routing when backends are unhealthy
        mock_request.url.path = "/v1/models"
        result = await proxy_middleware.dispatch(mock_request, call_next)
        
        # Should return 503, not call call_next
        assert result.status_code == 503
        assert b"vLLM backend not ready" in result.body
        call_next.assert_not_called()
    finally:
        # Restore original state
        vllm_backend_ports.clear()
        vllm_backend_ports.extend(original_backends)
        vllm_healthy.clear()
        vllm_healthy.update(original_healthy)


def test_setup_vllm_proxy():
    """Test vLLM proxy setup."""
    backend_ports = [5001, 5002, 5003]
    
    setup_vllm_proxy(backend_ports)
    
    # Import here to get the updated global state
    from api.proxy import vllm_backend_ports, vllm_counts, vllm_healthy
    
    assert vllm_backend_ports == backend_ports
    assert all(port in vllm_counts for port in backend_ports)
    assert all(port in vllm_healthy for port in backend_ports)


@pytest.mark.asyncio
async def test_start_stop_vllm_proxy():
    """Test vLLM proxy start and stop."""
    
    # Test start
    await start_vllm_proxy()
    
    # Import here to get the updated global state
    from api.proxy import vllm_client
    assert vllm_client is not None
    
    # Test stop
    await stop_vllm_proxy()
    
    # Import again to get the updated state
    from api.proxy import vllm_client
    assert vllm_client is None 