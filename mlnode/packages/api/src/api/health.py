import time
from typing import List, Dict, Any

from fastapi import APIRouter, Request, Response
from pydantic import BaseModel

from api.gpu.types import GPUDevice
from api.service_management import ServiceState

router = APIRouter()

# Simple in-memory cache
cache: Dict[str, Any] = {"data": None, "timestamp": 0}
CACHE_TTL = 5.0  # 5 seconds


class GPUInfo(BaseModel):
    available: bool
    count: int
    devices: List[GPUDevice]


class ManagerStatus(BaseModel):
    running: bool
    healthy: bool


class ManagersInfo(BaseModel):
    pow: ManagerStatus
    inference: ManagerStatus
    train: ManagerStatus


class HealthResponse(BaseModel):
    status: str
    service_state: ServiceState
    gpu: GPUInfo
    managers: ManagersInfo

class ReadinessResponse(BaseModel):
    ready: bool


async def get_health_data(request: Request) -> HealthResponse:
    """Gathers health data from all relevant managers."""
    # GPU Manager
    gpu_manager = request.app.state.gpu_manager
    gpu_devices = await gpu_manager.get_devices_async()
    gpu_available = await gpu_manager.is_cuda_available_async()
    gpu_info = GPUInfo(
        available=gpu_available,
        count=len(gpu_devices),
        devices=gpu_devices,
    )

    # Service Managers
    pow_manager = request.app.state.pow_manager
    inference_manager = request.app.state.inference_manager
    train_manager = request.app.state.train_manager

    managers_info = ManagersInfo(
        pow=ManagerStatus(
            running=pow_manager.is_running(), healthy=pow_manager.is_healthy()
        ),
        inference=ManagerStatus(
            running=inference_manager.is_running(),
            healthy=inference_manager.is_healthy(),
        ),
        train=ManagerStatus(
            running=train_manager.is_running(), healthy=train_manager.is_healthy()
        ),
    )

    # Determine overall status
    overall_healthy = True
    
    # GPU must be available for healthy status
    if not gpu_info.available:
        overall_healthy = False
    
    if managers_info.pow.running and not managers_info.pow.healthy:
        overall_healthy = False
    if managers_info.inference.running and not managers_info.inference.healthy:
        overall_healthy = False

    return HealthResponse(
        status="healthy" if overall_healthy else "unhealthy",
        service_state=request.app.state.service_state,
        gpu=gpu_info,
        managers=managers_info,
    )


@router.get("/health", response_model=HealthResponse, tags=["Health"])
@router.get("/livez", response_model=HealthResponse, tags=["Health"])
async def get_liveness(request: Request, response: Response):
    """Provides a detailed health check of the entire service."""
    current_time = time.time()
    if current_time - cache["timestamp"] < CACHE_TTL and cache["data"]:
        cached_response = HealthResponse(**cache["data"])
        if cached_response.status != "healthy":
            response.status_code = 503
        return cached_response

    health_data = await get_health_data(request)
    
    # Update cache
    cache["data"] = health_data.model_dump()
    cache["timestamp"] = current_time

    if health_data.status != "healthy":
        response.status_code = 503  # Service Unavailable
    
    return health_data


@router.get("/readyz", response_model=ReadinessResponse, tags=["Health"])
async def get_readiness(request: Request, response: Response):
    """
    Indicates whether the service is ready to accept traffic.
    Returns 200 if ready, 503 if not.
    """
    inference_manager = request.app.state.inference_manager
    
    is_ready = True
    # If inference is the active service, readiness depends on its health
    if request.app.state.service_state == ServiceState.INFERENCE:
        if not inference_manager.is_healthy():
            is_ready = False
    
    # Add similar checks for POW and TRAIN if they have specific readiness requirements
    # For now, we assume they are ready if running.

    if not is_ready:
        response.status_code = 503

    return ReadinessResponse(ready=is_ready)
