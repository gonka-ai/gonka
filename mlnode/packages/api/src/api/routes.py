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


class StateResponse(BaseModel):
    state: ServiceState
    version: str = _MLNODE_VERSION
    poc_status: Optional[str] = None          # "IDLE" | "GENERATING" | "VALIDATING" | "MIXED" | "NO_BACKENDS"
    inference_healthy: Optional[bool] = None  # True when ≥1 vLLM backend is up
    loaded_model: Optional[str] = None        # Model the current vLLM process was started with


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

    return StateResponse(
        state=current_state,
        poc_status=poc_status,
        inference_healthy=True,
        loaded_model=loaded_model,
    )

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
