"""PoC v2 routes for MLNode - proxies to vLLM PoC API with multi-backend support."""
import asyncio
from typing import List, Optional

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict

from common.logger import create_logger
from api.proxy import (
    get_healthy_backends,
    pick_backend_for_pow_generate,
    call_backend,
    VLLM_HOST,
)

logger = create_logger(__name__)

router = APIRouter(prefix="/inference/pow", tags=["PoC v2"])


# Request/Response Models (matching vLLM PoC v2 API)

class PoCParamsModel(BaseModel):
    model_config = ConfigDict(extra="forbid")
    model: str
    seq_len: int
    k_dim: int = 12


class PoCInitGenerateRequest(BaseModel):
    """MLNode /init/generate request - group_id/n_groups omitted (injected by MLNode)."""
    block_hash: str
    block_height: int
    public_key: str
    node_id: int
    node_count: int
    batch_size: int = 32
    params: PoCParamsModel
    url: Optional[str] = None


class ArtifactModel(BaseModel):
    nonce: int
    vector_b64: str


class ValidationModel(BaseModel):
    artifacts: List[ArtifactModel]


class StatTestModel(BaseModel):
    dist_threshold: float = 0.02
    p_mismatch: float = 0.001
    fraud_threshold: float = 0.01


class PoCGenerateRequest(BaseModel):
    """Request for /generate endpoint."""
    block_hash: str
    block_height: int
    public_key: str
    node_id: int
    node_count: int
    nonces: List[int]
    params: PoCParamsModel
    batch_size: int = 32
    wait: bool = False
    url: Optional[str] = None
    validation: Optional[ValidationModel] = None
    stat_test: Optional[StatTestModel] = None


# Endpoints

@router.post("/init/generate")
async def init_generate(body: PoCInitGenerateRequest) -> dict:
    """Fan-out /init/generate to all healthy backends with group_id injection."""
    backends = get_healthy_backends()
    if not backends:
        raise HTTPException(status_code=503, detail="No vLLM backends available")
    
    n_groups = len(backends)
    results = []
    errors = []
    
    async def call_one(port: int, group_id: int):
        payload = body.model_dump()
        payload["group_id"] = group_id
        payload["n_groups"] = n_groups
        try:
            r = await call_backend(port, "POST", "/api/v1/pow/init/generate", payload)
            return port, r.status_code, r.json() if r.status_code == 200 else r.text
        except Exception as e:
            return port, 500, str(e)
    
    tasks = [call_one(port, i) for i, port in enumerate(backends)]
    for coro in asyncio.as_completed(tasks):
        port, status, data = await coro
        if status == 200:
            results.append({"port": port, "status": "OK"})
        else:
            errors.append({"port": port, "error": data})
    
    if not results:
        raise HTTPException(status_code=502, detail={"errors": errors})
    
    return {
        "status": "OK",
        "backends": len(results),
        "n_groups": n_groups,
        "results": results,
        "errors": errors if errors else None,
    }


@router.post("/stop")
async def stop() -> dict:
    """Fan-out /stop to all healthy backends."""
    backends = get_healthy_backends()
    if not backends:
        return {"status": "OK", "message": "No backends to stop"}
    
    results = []
    errors = []
    
    async def call_one(port: int):
        try:
            r = await call_backend(port, "POST", "/api/v1/pow/stop", {})
            return port, r.status_code, r.json() if r.status_code == 200 else r.text
        except Exception as e:
            return port, 500, str(e)
    
    tasks = [call_one(port) for port in backends]
    for coro in asyncio.as_completed(tasks):
        port, status, data = await coro
        if status == 200:
            results.append({"port": port, "status": "stopped"})
        else:
            errors.append({"port": port, "error": data})
    
    return {
        "status": "OK",
        "results": results,
        "errors": errors if errors else None,
    }


@router.get("/status")
async def status() -> dict:
    """Aggregate /status from all healthy backends."""
    backends = get_healthy_backends()
    if not backends:
        return {"status": "NO_BACKENDS", "backends": []}
    
    backend_statuses = []
    
    async def call_one(port: int):
        try:
            r = await call_backend(port, "GET", "/api/v1/pow/status")
            if r.status_code == 200:
                data = r.json()
                return port, data
            return port, {"status": "ERROR", "detail": r.text}
        except Exception as e:
            return port, {"status": "ERROR", "detail": str(e)}
    
    tasks = [call_one(port) for port in backends]
    for coro in asyncio.as_completed(tasks):
        port, data = await coro
        backend_statuses.append({"port": port, **data})
    
    # Determine aggregate status
    statuses = [b.get("status", "UNKNOWN") for b in backend_statuses]
    if all(s == "GENERATING" for s in statuses):
        agg_status = "GENERATING"
    elif any(s == "GENERATING" for s in statuses):
        agg_status = "MIXED"
    elif all(s == "IDLE" for s in statuses):
        agg_status = "IDLE"
    else:
        agg_status = "MIXED"
    
    return {
        "status": agg_status,
        "backends": backend_statuses,
    }


@router.post("/generate")
async def generate(body: PoCGenerateRequest) -> dict:
    """Route /generate to a backend using round-robin."""
    try:
        port = await pick_backend_for_pow_generate()
    except RuntimeError:
        raise HTTPException(status_code=503, detail="No vLLM backends available")
    
    try:
        r = await call_backend(port, "POST", "/api/v1/pow/generate", body.model_dump())
        
        if r.status_code != 200:
            raise HTTPException(status_code=r.status_code, detail=r.text)
        
        data = r.json()
        
        # For queued requests, create composite request_id
        if data.get("status") == "queued" and "request_id" in data:
            data["request_id"] = f"{port}:{data['request_id']}"
        
        return data
        
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=str(e))


@router.get("/generate/{request_id:path}")
async def get_generate_result(request_id: str) -> dict:
    """Poll for result of queued /generate, routing to correct backend via composite id."""
    if ":" not in request_id:
        raise HTTPException(status_code=400, detail="Invalid request_id format (expected port:uuid)")
    
    port_str, backend_request_id = request_id.split(":", 1)
    try:
        port = int(port_str)
    except ValueError:
        raise HTTPException(status_code=400, detail="Invalid port in request_id")
    
    try:
        r = await call_backend(port, "GET", f"/api/v1/pow/generate/{backend_request_id}")
        
        if r.status_code == 404:
            raise HTTPException(status_code=404, detail="Request not found")
        if r.status_code != 200:
            raise HTTPException(status_code=r.status_code, detail=r.text)
        
        data = r.json()
        # Preserve composite request_id in response
        data["request_id"] = request_id
        return data
        
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=502, detail=str(e))
