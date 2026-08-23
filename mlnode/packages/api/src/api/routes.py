from typing import Optional
from importlib.metadata import version as pkg_version, PackageNotFoundError

from fastapi import APIRouter, Request
from pydantic import BaseModel

from api.service_management import (
    ServiceState,
    update_service_state
)
from pow.service.manager import PowManager
from api.inference.manager import InferenceManager
from zeroband.service.manager import TrainManager
from common.logger import create_logger
import api.proxy as proxy_module

logger = create_logger(__name__)

_MLNODE_VERSION = "unknown"
try:
    _MLNODE_VERSION = pkg_version("mlnode-api")
except PackageNotFoundError:
    logger.warning("mlnode-api package metadata not found, version will be reported as 'unknown'")

router = APIRouter(
    tags=["API v1"],
)


class VersionedResponse(BaseModel):
    version: str = _MLNODE_VERSION


class StateResponse(VersionedResponse):
    state: ServiceState
    poc_status: Optional[str] = None          # "IDLE" | "GENERATING" | "VALIDATING" | "MIXED" | "NO_BACKENDS"
    inference_healthy: Optional[bool] = None  # True when ≥1 vLLM backend is up
    loaded_model: Optional[str] = None        # Model the current vLLM process was started with
    poc_validation_inference: bool = False


class VersionsResponse(VersionedResponse):
    vllm_version: Optional[str] = None
    poc_validation_inference: bool = False


_vllm_versions_cache: Optional[VersionsResponse] = None


async def _query_vllm_versions(backends: Optional[list[int]] = None) -> VersionsResponse:
    global _vllm_versions_cache

    if _vllm_versions_cache is not None:
        return _vllm_versions_cache

    backend_ports = backends if backends is not None else proxy_module.get_healthy_backends()
    if not backend_ports:
        return VersionsResponse()

    try:
        response = await proxy_module.call_backend(backend_ports[0], "GET", "/api/v1/pow/versions")
        if response.status_code >= 400:
            logger.debug("vLLM /api/v1/pow/versions returned status %s", response.status_code)
            return VersionsResponse()
        data = response.json()
        if not isinstance(data, dict):
            logger.debug("vLLM /api/v1/pow/versions returned non-object response")
            return VersionsResponse()
    except Exception as exc:
        logger.debug("failed to query vLLM versions endpoint: %s", exc)
        return VersionsResponse()

    vllm_version = data.get("vllm_version")
    _vllm_versions_cache = VersionsResponse(
        vllm_version=vllm_version if isinstance(vllm_version, str) else None,
        poc_validation_inference=data.get("poc_validation_inference") is True,
    )
    return _vllm_versions_cache


@router.get("/state")
async def state(request: Request) -> StateResponse:
    await update_service_state(request)
    current_state: ServiceState = request.app.state.service_state

    if current_state != ServiceState.INFERENCE:
        return StateResponse(state=current_state)

    healthy_ports = [p for p, ok in proxy_module.vllm_healthy.items() if ok]
    if not healthy_ports:
        return StateResponse(
            state=current_state,
            poc_status="NO_BACKENDS",
            inference_healthy=False,
        )

    statuses = [proxy_module.poc_status_by_port.get(p, "") for p in healthy_ports]
    active = {"GENERATING", "VALIDATING"}
    if all(s == "GENERATING" for s in statuses):
        poc_status = "GENERATING"
    elif all(s == "VALIDATING" for s in statuses):
        poc_status = "VALIDATING"
    elif any(s in active for s in statuses):
        poc_status = "MIXED"
    else:
        poc_status = "IDLE"

    runner = getattr(request.app.state.inference_manager, "vllm_runner", None)
    loaded_model = getattr(runner, "model", None) if runner is not None else None
    versions_response = await _query_vllm_versions(healthy_ports)

    return StateResponse(
        state=current_state,
        poc_status=poc_status,
        inference_healthy=True,
        loaded_model=loaded_model,
        poc_validation_inference=versions_response.poc_validation_inference,
    )


@router.get("/versions")
async def versions() -> VersionsResponse:
    return await _query_vllm_versions()


@router.post("/stop")
async def stop(request: Request):
    pow_manager: PowManager = request.app.state.pow_manager
    inference_manager: InferenceManager = request.app.state.inference_manager
    train_manager: TrainManager = request.app.state.train_manager

    if pow_manager.is_running():
        pow_manager.stop()
    if inference_manager.is_running():
        # Use async stop in async context to avoid blocking event loop
        await inference_manager._async_stop()
    if train_manager.is_running():
        train_manager.stop()

    return {"status": "OK"}
