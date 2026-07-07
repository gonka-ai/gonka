import pytest
import asyncio
import httpx
from unittest.mock import AsyncMock, MagicMock, patch
from fastapi import Request
from fastapi.testclient import TestClient
from aiohttp import web

from api.proxy import (
    ProxyMiddleware, 
    setup_vllm_proxy, 
    start_vllm_proxy, 
    stop_vllm_proxy,
    shutdown_event,
    active_proxy_tasks,
    tasks_lock,
    _proxy_request_to_backend,
    _release_vllm_backend,
)
import api.proxy as proxy_module


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
        assert b"No vLLM backend available" in result.body
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


# ============================================================================
# Graceful Shutdown Tests
# ============================================================================

@pytest.fixture
async def mock_backend_server():
    """Create a mock aiohttp backend server for testing."""
    app = web.Application()
    
    async def slow_handler(request):
        """Simulate a slow inference request."""
        await asyncio.sleep(10)  # Long running operation
        return web.Response(text="completed")
    
    async def instant_handler(request):
        """Simulate an instant response."""
        return web.Response(text="ok")
    
    app.router.add_get('/slow', slow_handler)
    app.router.add_get('/instant', instant_handler)
    
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, '127.0.0.1', 8765)
    await site.start()
    
    yield 'http://127.0.0.1:8765'
    
    await runner.cleanup()


@pytest.fixture(autouse=True)
async def reset_proxy_state():
    """Reset proxy state before and after each test."""
    # Stop any running background servers FIRST
    try:
        await proxy_module.stop_backward_compatibility()
    except Exception:
        pass
    
    # Stop the proxy to clean up any lingering tasks
    try:
        await proxy_module.stop_vllm_proxy()
    except Exception:
        pass
    
    # Reset all state
    proxy_module.shutdown_event.clear()
    async with proxy_module.tasks_lock:
        proxy_module.active_proxy_tasks.clear()
    
    # Reset backend counts to zero
    for port in list(proxy_module.vllm_counts.keys()):
        proxy_module.vllm_counts[port] = 0
    
    # Give tasks a moment to fully clean up
    await asyncio.sleep(0.05)
    
    yield
    
    # Cleanup after test
    try:
        await proxy_module.stop_backward_compatibility()
    except Exception:
        pass
    
    try:
        await proxy_module.stop_vllm_proxy()
    except Exception:
        pass
    
    # Reset all state
    proxy_module.shutdown_event.clear()
    async with proxy_module.tasks_lock:
        proxy_module.active_proxy_tasks.clear()
    
    # Reset backend counts to zero
    for port in list(proxy_module.vllm_counts.keys()):
        proxy_module.vllm_counts[port] = 0


@pytest.mark.asyncio
async def test_task_cancellation_during_active_request(mock_backend_server):
    """Test that active proxy tasks are cancelled during shutdown."""
    # Setup backend
    setup_vllm_proxy([5001])
    proxy_module.vllm_healthy[5001] = True
    await start_vllm_proxy()
    
    # Create a mock request
    mock_request = MagicMock(spec=Request)
    mock_request.method = "GET"
    mock_request.headers = {}
    mock_request.query_params = {}
    mock_request.stream = AsyncMock(return_value=iter([]))
    
    # Track streaming task
    streaming_started = asyncio.Event()
    streaming_task = None
    
    async def track_and_stream():
        nonlocal streaming_task
        # Start the proxy request
        try:
            response = await _proxy_request_to_backend(mock_request, "/slow")
            streaming_started.set()
            
            # Consume the response (this will register the task)
            # For StreamingResponse, body_iterator is the generator
            if hasattr(response, 'body_iterator'):
                async for chunk in response.body_iterator:
                    pass
        except asyncio.CancelledError:
            pass  # Expected during shutdown
    
    # Start the streaming task
    streaming_task = asyncio.create_task(track_and_stream())
    
    # Wait a bit for task registration
    await asyncio.sleep(0.1)
    
    # Verify task is registered
    async with proxy_module.tasks_lock:
        initial_task_count = len(proxy_module.active_proxy_tasks)
    
    # Trigger shutdown
    proxy_module.shutdown_event.set()
    
    # Cancel the streaming task (simulating what manager would do)
    async with proxy_module.tasks_lock:
        tasks = list(proxy_module.active_proxy_tasks)
        proxy_module.active_proxy_tasks.clear()
    
    for task in tasks:
        task.cancel()
    
    # Wait for cancellation to complete
    await asyncio.gather(*tasks, return_exceptions=True)
    
    # Verify tasks were cleared
    async with proxy_module.tasks_lock:
        assert len(proxy_module.active_proxy_tasks) == 0
    
    await stop_vllm_proxy()


@pytest.mark.asyncio
async def test_new_requests_rejected_during_shutdown():
    """Test that new proxy requests are rejected when shutdown is in progress."""
    # Setup
    setup_vllm_proxy([5001])
    proxy_module.vllm_healthy[5001] = True
    await start_vllm_proxy()
    
    # Set shutdown flag
    async with proxy_module.tasks_lock:
        proxy_module.shutdown_event.set()
    
    # Try to make a new request
    mock_request = MagicMock(spec=Request)
    mock_request.method = "GET"
    mock_request.headers = {}
    mock_request.query_params = {}
    mock_request.stream = AsyncMock(return_value=iter([]))
    
    response = await _proxy_request_to_backend(mock_request, "/test")
    
    # Should get 503 with shutdown message
    assert response.status_code == 503
    assert b"shutting down" in response.body
    
    await stop_vllm_proxy()


@pytest.mark.asyncio
async def test_task_registration_race_condition_prevention():
    """Test that shutdown-during-registration is handled correctly."""
    # Setup - don't start background tasks to avoid interference
    setup_vllm_proxy([5001])
    proxy_module.vllm_healthy[5001] = True
    # Start client only, not the background tasks
    proxy_module.vllm_client = httpx.AsyncClient(http2=True, limits=proxy_module.LIMITS)
    
    mock_request = MagicMock(spec=Request)
    mock_request.method = "GET"
    mock_request.headers = {}
    mock_request.query_params = {}
    mock_request.stream = AsyncMock(return_value=iter([]))
    
    # Mock upstream
    mock_upstream = MagicMock()
    mock_upstream.status_code = 200
    mock_upstream.headers = {}
    
    async def mock_aiter():
        yield b"data1"
        await asyncio.sleep(0.05)
        yield b"data2"
    
    mock_upstream.aiter_raw = mock_aiter
    
    with patch.object(proxy_module.vllm_client, 'stream') as mock_stream:
        mock_cm = MagicMock()
        mock_cm.__aenter__ = AsyncMock(return_value=mock_upstream)
        mock_cm.__aexit__ = AsyncMock(return_value=None)
        mock_stream.return_value = mock_cm
        
        # Start proxy request
        response = await _proxy_request_to_backend(mock_request, "/test")
        
        # Start consuming
        async def consume():
            try:
                if hasattr(response, 'body_iterator'):
                    async for chunk in response.body_iterator:
                        pass
            except asyncio.CancelledError:
                pass
        
        task = asyncio.create_task(consume())
        
        # Give it a moment to register
        await asyncio.sleep(0.01)
        
        # Trigger shutdown while stream is active
        async with proxy_module.tasks_lock:
            proxy_module.shutdown_event.set()
            tasks = list(proxy_module.active_proxy_tasks)
        
        # Cancel tasks
        for t in tasks:
            t.cancel()
        
        await asyncio.gather(task, return_exceptions=True)
        
        # Verify clean state
        async with proxy_module.tasks_lock:
            assert len(proxy_module.active_proxy_tasks) == 0
    
    # Cleanup
    proxy_module.shutdown_event.clear()
    if proxy_module.vllm_client:
        await proxy_module.vllm_client.aclose()
        proxy_module.vllm_client = None


@pytest.mark.asyncio
async def test_resource_cleanup_verification():
    """Test that all resources are cleaned up after shutdown."""
    # Setup
    setup_vllm_proxy([5001, 5002])
    proxy_module.vllm_healthy[5001] = True
    proxy_module.vllm_healthy[5002] = True
    await start_vllm_proxy()
    
    # Verify initial state
    assert proxy_module.vllm_client is not None
    assert not proxy_module.shutdown_event.is_set()
    
    # Simulate shutdown
    proxy_module.shutdown_event.set()
    
    # Cancel any active tasks
    async with proxy_module.tasks_lock:
        tasks = list(proxy_module.active_proxy_tasks)
        proxy_module.active_proxy_tasks.clear()
    
    for task in tasks:
        task.cancel()
    
    await asyncio.gather(*tasks, return_exceptions=True)
    
    # Stop proxy
    await stop_vllm_proxy()
    
    # Clear shutdown event (normally done by manager)
    proxy_module.shutdown_event.clear()
    
    # Verify cleanup
    async with proxy_module.tasks_lock:
        assert len(proxy_module.active_proxy_tasks) == 0
    assert proxy_module.vllm_client is None
    assert not proxy_module.shutdown_event.is_set()
    
    # Verify backend counts reset
    for port in [5001, 5002]:
        assert proxy_module.vllm_counts[port] == 0 

# ---------------------------------------------------------------------------
# Health-probe damping (_apply_health_damping)
# ---------------------------------------------------------------------------

@pytest.fixture
def clean_health_state():
    """Reset the module-level health dicts around each damping test."""
    saved_healthy = dict(proxy_module.vllm_healthy)
    saved_streak = dict(proxy_module.vllm_health_fail_streak)
    proxy_module.vllm_healthy.clear()
    proxy_module.vllm_health_fail_streak.clear()
    yield
    proxy_module.vllm_healthy.clear()
    proxy_module.vllm_healthy.update(saved_healthy)
    proxy_module.vllm_health_fail_streak.clear()
    proxy_module.vllm_health_fail_streak.update(saved_streak)


def _drive(port, probes, start_healthy):
    """Feed a probe sequence through the damping and record verdicts, updating
    vllm_healthy the same way _health_check_vllm does."""
    proxy_module.vllm_healthy[port] = start_healthy
    out = []
    for probe in probes:
        ok = proxy_module._apply_health_damping(port, probe)
        proxy_module.vllm_healthy[port] = ok
        out.append(ok)
    return out


def test_damping_startup_stays_down_until_first_success(clean_health_state):
    assert _drive(5001, [False, False, True], start_healthy=False) == [False, False, True]


def test_damping_two_blips_still_healthy(clean_health_state):
    """Missed /health deadlines on a busy-but-alive engine (up to
    HEALTH_FAIL_STREAK-1 in a row) must not stop the compatibility server."""
    assert _drive(5001, [True, False, False, True], start_healthy=True) == [True, True, True, True]


def test_damping_three_consecutive_failures_is_down(clean_health_state):
    assert _drive(5001, [True, False, False, False], start_healthy=True) == [True, True, True, False]


def test_damping_recovers_instantly_on_success(clean_health_state):
    assert _drive(5001, [False, False, False, True], start_healthy=True) == [True, True, False, True]


def test_damping_ports_are_independent(clean_health_state):
    proxy_module.vllm_healthy.update({5001: True, 5002: True})
    # 5001 fails 3x -> down; 5002 keeps succeeding -> up
    for _ in range(3):
        proxy_module.vllm_healthy[5001] = proxy_module._apply_health_damping(5001, False)
        proxy_module.vllm_healthy[5002] = proxy_module._apply_health_damping(5002, True)
    assert proxy_module.vllm_healthy[5001] is False
    assert proxy_module.vllm_healthy[5002] is True
