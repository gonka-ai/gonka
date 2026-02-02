import asyncio
import os
from typing import Dict, List, Optional, Set

import uvicorn
import httpx
from fastapi import FastAPI, Request, Response
from fastapi.responses import StreamingResponse
from starlette.background import BackgroundTask
from starlette.middleware.base import BaseHTTPMiddleware

from common.logger import create_logger

logger = create_logger(__name__)

VLLM_HOST = "127.0.0.1"

LIMITS = httpx.Limits(
    max_connections=20_000,
    max_keepalive_connections=5_000,
)

vllm_backend_ports: List[int] = []
vllm_healthy: Dict[int, bool] = {}
vllm_counts: Dict[int, int] = {}
poc_status_by_port: Dict[int, str] = {}  # PoC status: "IDLE", "GENERATING", "STOPPED", or ""
pow_generate_rr_index: int = 0
vllm_pick_lock = asyncio.Lock()
vllm_client: Optional[httpx.AsyncClient] = None

shutdown_event = asyncio.Event()
active_proxy_tasks: Set[asyncio.Task] = set()
tasks_lock = asyncio.Lock()

compatibility_app: Optional[FastAPI] = None
compatibility_server_task: Optional[asyncio.Task] = None
compatibility_server: Optional[object] = None  # uvicorn.Server instance
health_check_task: Optional[asyncio.Task] = None


class ProxyMiddleware(BaseHTTPMiddleware):
    """Middleware to handle routing between /api and /v1 endpoints."""
    
    async def dispatch(self, request: Request, call_next):
        path = request.url.path
        
        if path.startswith("/v1"):
            return await self._proxy_to_vllm(request)
        
        if path.startswith("/api"):
            return await call_next(request)
        
        return await call_next(request)
    
    async def _proxy_to_vllm(self, request: Request) -> Response:
        """Proxy requests to vLLM backend with load balancing."""
        return await _proxy_request_to_backend(request, request.url.path)


async def _proxy_request_to_backend(request: Request, backend_path: str) -> Response:
    logger.debug(f"Proxying request to backend: {request.method} {backend_path}")
    
    if not vllm_backend_ports or not any(vllm_healthy.values()):
        logger.warning(f"No vLLM backend available. Ports: {vllm_backend_ports}, Healthy: {vllm_healthy}")
        return Response(status_code=503, content=b"No vLLM backend available")
    
    if shutdown_event.is_set():
        return Response(status_code=503, content=b"Service is shutting down")
    
    try:
        port = await _pick_vllm_backend()
    except RuntimeError:
        return Response(status_code=503, content=b"No vLLM backend available")
    
    if not backend_path.startswith("/"):
        backend_path = "/" + backend_path
    url = f"http://{VLLM_HOST}:{port}{backend_path}"
    headers = {k: v for k, v in request.headers.items() if k.lower() != "host"}
    
    if vllm_client is None:
        await _release_vllm_backend(port)
        return Response(status_code=503, content=b"vLLM client not initialized")
    
    try:
        context_manager = vllm_client.stream(
            request.method,
            url,
            params=request.query_params,
            headers=headers,
            content=request.stream(),
            timeout=httpx.Timeout(None, read=900),
        )
        upstream = await context_manager.__aenter__()
    except Exception as exc:
        logger.exception(f"Failed to connect to vLLM backend: {exc}")
        await _release_vllm_backend(port)
        return Response(status_code=502, content=b"vLLM connection failed")
    
    resp_headers = {
        k: v for k, v in upstream.headers.items()
        if k.lower() not in {"content-length", "transfer-encoding", "connection"}
    }
    
    async def stream_with_tracking():
        current_task = asyncio.current_task()
        
        if current_task:
            async with tasks_lock:
                if shutdown_event.is_set():
                    raise asyncio.CancelledError("Shutdown in progress")
                active_proxy_tasks.add(current_task)
        
        try:
            async for chunk in upstream.aiter_raw():
                yield chunk
                
        except asyncio.CancelledError:
            logger.info(f"Stream cancelled for port {port} during shutdown")
            raise
        
        except Exception as exc:
            logger.exception(f"vLLM streaming error on port {port}: {exc}")
            raise
            
        finally:
            if current_task:
                async with tasks_lock:
                    active_proxy_tasks.discard(current_task)
            
            try:
                await context_manager.__aexit__(None, None, None)
            except Exception as e:
                logger.error(f"Error closing upstream connection: {e}")
            
            await _release_vllm_backend(port)
    
    return StreamingResponse(
        stream_with_tracking(),
        status_code=upstream.status_code,
        headers=resp_headers,
    )


async def _pick_vllm_backend() -> int:
    """Least-connections picker for vLLM backends."""
    async with vllm_pick_lock:
        live = [p for p, ok in vllm_healthy.items() if ok]
        if not live:
            raise RuntimeError("no vLLM backend")
        port = min(live, key=lambda p: vllm_counts.get(p, 0))
        vllm_counts[port] += 1
        return port


async def _release_vllm_backend(port: int):
    """Release a vLLM backend connection."""
    async with vllm_pick_lock:
        vllm_counts[port] -= 1


def get_healthy_backends() -> List[int]:
    """Return list of healthy backend ports in stable order."""
    return sorted([p for p, ok in vllm_healthy.items() if ok])


async def pick_backend_for_pow_generate() -> int:
    """Round-robin picker for PoW /generate requests."""
    global pow_generate_rr_index
    async with vllm_pick_lock:
        live = sorted([p for p, ok in vllm_healthy.items() if ok])
        if not live:
            raise RuntimeError("no vLLM backend")
        port = live[pow_generate_rr_index % len(live)]
        pow_generate_rr_index += 1
        return port


async def call_backend(port: int, method: str, path: str, json_body: dict = None) -> httpx.Response:
    """Make a direct call to a specific backend port."""
    if vllm_client is None:
        raise RuntimeError("vLLM client not initialized")
    
    url = f"http://{VLLM_HOST}:{port}{path}"
    if method.upper() == "GET":
        return await vllm_client.get(url, timeout=60)
    elif method.upper() == "POST":
        return await vllm_client.post(url, json=json_body, timeout=60)
    else:
        raise ValueError(f"Unsupported method: {method}")


async def _health_check_vllm(interval: float = 2.0):
    """Health check for vLLM backends and manage compatibility server."""
    logger.info("Health check loop started, checking every %s seconds", interval)
    while True:
        if not vllm_backend_ports:
            # No backends configured yet, wait and check again
            logger.debug("No backend ports configured yet, waiting...")
            await asyncio.sleep(interval)
            continue
            
        for p in vllm_backend_ports:
            ok = False
            try:
                if vllm_client is None:
                    logger.warning(f"Health check skipped for port {p}: vllm_client is None")
                    continue
                r = await vllm_client.get(f"http://{VLLM_HOST}:{p}/health", timeout=2)
                ok = r.status_code == 200
                logger.debug("Health check for port %d: status=%d, ok=%s", p, r.status_code, ok)
            except Exception as e:
                logger.debug("Health check for port %d failed: %s", p, e)

            prev = vllm_healthy.get(p)
            if prev != ok:
                logger.info("%s:%d is %s", VLLM_HOST, p, "UP" if ok else "DOWN")
            vllm_healthy[p] = ok
            
            # Poll PoC status for healthy backends
            if ok:
                try:
                    r = await vllm_client.get(f"http://{VLLM_HOST}:{p}/api/v1/pow/status", timeout=2)
                    if r.status_code == 200:
                        data = r.json()
                        poc_status_by_port[p] = data.get("status", "")
                    else:
                        poc_status_by_port[p] = ""
                except Exception:
                    poc_status_by_port[p] = ""
            else:
                poc_status_by_port[p] = ""
        
        # Manage backward compatibility server based on backend health
        has_healthy_backends = any(vllm_healthy.values())
        
        if compatibility_server_task and not has_healthy_backends:
            logger.info("No vLLM backends healthy, stopping backward compatibility server")
            await stop_backward_compatibility()
        elif not compatibility_server_task and has_healthy_backends:
            logger.info("vLLM backends are healthy, starting backward compatibility server")
            await start_backward_compatibility()
        
        await asyncio.sleep(interval)


def setup_vllm_proxy(backend_ports: List[int]):
    """Setup vLLM proxy with given backend ports."""
    global vllm_backend_ports, vllm_counts
    vllm_backend_ports = backend_ports
    vllm_counts = {p: 0 for p in vllm_backend_ports}
    vllm_healthy.update({p: False for p in vllm_backend_ports})
    poc_status_by_port.update({p: "" for p in vllm_backend_ports})
    logger.info("vLLM proxy setup with %d backends: %s", len(backend_ports), backend_ports)
    logger.debug("vLLM backend ports: %s", vllm_backend_ports)
    logger.debug("vLLM healthy status: %s", vllm_healthy)



async def start_vllm_proxy():
    """Start vLLM proxy components."""
    global vllm_client, health_check_task
    vllm_client = httpx.AsyncClient(http2=True, limits=LIMITS)
    # Health check monitors backends and manages compatibility server automatically
    health_check_task = asyncio.create_task(_health_check_vllm())
    logger.info("vLLM proxy started")


async def stop_vllm_proxy():
    """Stop vLLM proxy components."""
    global vllm_client, health_check_task
    
    # Cancel health check task
    if health_check_task and not health_check_task.done():
        health_check_task.cancel()
        try:
            await health_check_task
        except asyncio.CancelledError:
            pass
        health_check_task = None
    
    # Stop compatibility server
    await stop_backward_compatibility()
    
    # Close HTTP client
    if vllm_client:
        await vllm_client.aclose()
        vllm_client = None
    logger.info("vLLM proxy stopped")


async def _compatibility_proxy_handler(request: Request, path: str):
    """Handler for backward compatibility server - proxies all requests to vLLM backends."""
    logger.debug(f"Compatibility server received request: {request.method} /{path}")
    return await _proxy_request_to_backend(request, path)


async def _run_compatibility_server():
    """Run the backward compatibility server on port 5000."""
    global compatibility_app, compatibility_server
    
    compatibility_app = FastAPI(title="vLLM Backward Compatibility Proxy")
    
    @compatibility_app.api_route("/{path:path}", methods=["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"])
    async def proxy_all(request: Request, path: str):
        return await _compatibility_proxy_handler(request, path)
    
    
    logger.info("Starting backward compatibility server on port 5000")
    config = uvicorn.Config(
        compatibility_app,
        host="0.0.0.0",
        port=5000,
        workers=1,
        timeout_keep_alive=300,
        log_level="info"
    )
    server = uvicorn.Server(config)
    compatibility_server = server  # Save reference for shutdown
    try:
        await server.serve()
    finally:
        compatibility_server = None


async def start_backward_compatibility():
    """Start backward compatibility server on port 5000."""
    global compatibility_server_task
    if compatibility_server_task is None:
        logger.info("Creating backward compatibility server task on port 5000...")
        compatibility_server_task = asyncio.create_task(_run_compatibility_server())
        # Give it a moment to start
        await asyncio.sleep(0.1)
        logger.info("Backward compatibility server task created")
    else:
        logger.debug("Backward compatibility server already running")


async def stop_backward_compatibility():
    """Stop backward compatibility server."""
    global compatibility_server_task, compatibility_app, compatibility_server
    if compatibility_server_task:
        # First, shutdown the uvicorn server gracefully
        if compatibility_server:
            try:
                compatibility_server.should_exit = True
                await asyncio.sleep(0.1)  # Give it a moment to start shutdown
            except Exception as e:
                logger.debug(f"Error during server shutdown signal: {e}")
        
        # Then cancel the task if it's still running
        if not compatibility_server_task.done():
            compatibility_server_task.cancel()
            try:
                await compatibility_server_task
            except (asyncio.CancelledError, RuntimeError, Exception) as e:
                logger.debug(f"Error awaiting cancelled compatibility server task: {e}")
        
        compatibility_server_task = None
        compatibility_server = None
        compatibility_app = None
        logger.info("Backward compatibility server stopped") 