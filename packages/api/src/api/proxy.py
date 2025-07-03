import asyncio
import os
from typing import Dict, List, Optional

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
vllm_pick_lock = asyncio.Lock()
vllm_client: Optional[httpx.AsyncClient] = None

compatibility_app: Optional[FastAPI] = None
compatibility_server_task: Optional[asyncio.Task] = None


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
    """Common proxy logic for routing requests to vLLM backends."""
    if not vllm_backend_ports:
        return Response(status_code=503, content=b"No vLLM backend available")
    
    if not any(vllm_healthy.values()):
        return Response(status_code=503, content=b"vLLM backend not ready")
    
    try:
        port = await _pick_vllm_backend()
    except RuntimeError:
        return Response(status_code=503, content=b"No vLLM backend available")

    async def iter_body():
        async for chunk in request.stream():
            yield chunk

    if not backend_path.startswith("/"):
        backend_path = "/" + backend_path
    url = f"http://{VLLM_HOST}:{port}{backend_path}"
    headers = {k: v for k, v in request.headers.items() if k.lower() != "host"}

    if vllm_client is None:
        return Response(status_code=503, content=b"vLLM client not initialized")

    try:
        cm = vllm_client.stream(
            request.method,
            url,
            params=request.query_params,
            headers=headers,
            content=iter_body(),
            timeout=httpx.Timeout(None, read=900),
        )

        upstream = await cm.__aenter__()

        resp_headers = {
            k: v
            for k, v in upstream.headers.items()
            if k.lower() not in {"content-length", "transfer-encoding", "connection"}
        }

        async def _cleanup(cxt, port_):
            try:
                await cxt.__aexit__(None, None, None)
            finally:
                await _release_vllm_backend(port_)

        return StreamingResponse(
            upstream.aiter_raw(),
            status_code=upstream.status_code,
            headers=resp_headers,
            background=BackgroundTask(_cleanup, cm, port),
        )

    except Exception as exc:
        logger.exception("vLLM proxy error: %s", exc)
        await _release_vllm_backend(port)
        return Response(status_code=502, content=b"vLLM upstream failure")


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


async def _health_check_vllm(interval: float = 5.0):
    """Health check for vLLM backends."""
    while True:
        if not vllm_backend_ports:
            # No backends configured yet, wait and check again
            await asyncio.sleep(interval)
            continue
            
        logger.debug("Health check running, backend ports: %s", vllm_backend_ports)
        for p in vllm_backend_ports:
            ok = False
            try:
                if vllm_client is None:
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
        logger.debug("Current healthy status: %s", vllm_healthy)
        
        if compatibility_server_task and not any(vllm_healthy.values()):
            logger.info("No vLLM backends healthy, stopping backward compatibility server")
            await stop_backward_compatibility()
        
        await asyncio.sleep(interval)


def setup_vllm_proxy(backend_ports: List[int]):
    """Setup vLLM proxy with given backend ports."""
    global vllm_backend_ports, vllm_counts
    vllm_backend_ports = backend_ports
    vllm_counts = {p: 0 for p in vllm_backend_ports}
    vllm_healthy.update({p: False for p in vllm_backend_ports})
    logger.info("vLLM proxy setup with %d backends: %s", len(backend_ports), backend_ports)
    logger.debug("vLLM backend ports: %s", vllm_backend_ports)
    logger.debug("vLLM healthy status: %s", vllm_healthy)



async def start_vllm_proxy():
    """Start vLLM proxy components."""
    global vllm_client
    vllm_client = httpx.AsyncClient(http2=True, limits=LIMITS)
    # Always start health check - it will monitor for new backends
    asyncio.create_task(_health_check_vllm())
    asyncio.create_task(_start_backward_compatibility_when_ready())
    logger.info("vLLM proxy started")


async def stop_vllm_proxy():
    """Stop vLLM proxy components."""
    global vllm_client
    if vllm_client:
        await vllm_client.aclose()
        vllm_client = None
    logger.info("vLLM proxy stopped")


async def _start_backward_compatibility_when_ready():
    """Start backward compatibility server when vLLM backends are ready."""
    while True:
        if any(vllm_healthy.values()):
            logger.info("vLLM backends are ready, starting backward compatibility server")
            await start_backward_compatibility()
            break
        logger.debug("Waiting for vLLM backends to be ready...")
        await asyncio.sleep(2)



async def _compatibility_proxy_handler(request: Request, path: str):
    """Handler for backward compatibility server - proxies all requests to vLLM backends."""
    return await _proxy_request_to_backend(request, path)


async def _run_compatibility_server():
    """Run the backward compatibility server on port 5000."""
    global compatibility_app
    
    compatibility_app = FastAPI(title="vLLM Backward Compatibility Proxy")
    
    @compatibility_app.api_route("/{path:path}", methods=["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"])
    async def proxy_all(request: Request, path: str):
        return await _compatibility_proxy_handler(request, path)
    
    import uvicorn
    
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
    await server.serve()


async def start_backward_compatibility():
    """Start backward compatibility server on port 5000."""
    global compatibility_server_task
    if compatibility_server_task is None:
        compatibility_server_task = asyncio.create_task(_run_compatibility_server())
        logger.info("Backward compatibility server started on port 5000")
    else:
        logger.debug("Backward compatibility server already running")


async def stop_backward_compatibility():
    """Stop backward compatibility server."""
    global compatibility_server_task, compatibility_app
    if compatibility_server_task:
        compatibility_server_task.cancel()
        try:
            await compatibility_server_task
        except asyncio.CancelledError:
            pass
        compatibility_server_task = None
        compatibility_app = None
        logger.info("Backward compatibility server stopped") 