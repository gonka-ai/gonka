import argparse
import asyncio
from typing import (
    Dict,
    List,
)


import httpx
import uvicorn
from fastapi import (
    FastAPI,
    Request,
    Response,
)

from common.logger import create_logger

app = FastAPI()

backend_ports: List[int] = []
backend_host = "127.0.0.1"
healthy: Dict[int, bool] = {}
counts: Dict[int, int] = {}

lock = asyncio.Lock()
client: httpx.AsyncClient | None = None
limits = httpx.Limits(
    max_connections=10_000,
    max_keepalive_connections=1000,
)
log = create_logger("proxy")


@app.api_route("/{path:path}", methods=["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"])
async def proxy(request: Request, path: str):
    async with lock:
        ports = [p for p, ok in healthy.items() if ok]
        if not ports:
            return Response(status_code=503, content=b"No backend")
        port = min(ports, key=counts.get)
        counts[port] += 1
    try:
        url = f"http://{backend_host}:{port}/{path}"
        body = await request.body()
        headers = {k: v for k, v in request.headers.items() if k.lower() != "host"}
        resp = await client.request(
            request.method,
            url,
            params=request.query_params,
            content=body,
            headers=headers,
            timeout=None,
        )
        return Response(
            content=resp.content,
            status_code=resp.status_code,
            headers=dict(resp.headers),
        )
    finally:
        async with lock:
            counts[port] -= 1


async def health(interval: float = 5.0):
    while True:
        for p in backend_ports:
            ok = False
            try:
                r = await client.get(
                    f"http://{backend_host}:{p}/health",
                    timeout=2,
                )
                ok = r.status_code == 200
            except Exception:
                pass
            if healthy.get(p, True) and not ok:
                log.warning("backend %s:%d down", backend_host, p)
            elif not healthy.get(p, False) and ok:
                log.info("backend %s:%d up", backend_host, p)
            healthy[p] = ok
        await asyncio.sleep(interval)


@app.on_event("startup")
async def start():
    global client
    client = httpx.AsyncClient(limits=limits)
    healthy.update({p: False for p in backend_ports})
    asyncio.create_task(health())


@app.on_event("shutdown")
async def stop():
    await client.aclose()

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--backend-ports", type=str, required=True)
    parser.add_argument("--host", type=str, default="0.0.0.0")
    args = parser.parse_args()

    global backend_ports, counts
    backend_ports = [int(p) for p in args.backend_ports.split(",") if p]
    counts = {p: 0 for p in backend_ports}

    uvicorn.run(
        app,
        host=args.host,
        port=args.port,
        limit_concurrency=10_000,
        timeout_keep_alive=60,
    )

if __name__ == "__main__":
    main()
