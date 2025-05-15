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

logger = create_logger(__name__)

router = APIRouter(
    tags=["API v1"],
)

class StateResponse(BaseModel):
    state: ServiceState

@router.get("/state")
async def state(request: Request) -> StateResponse:
    update_service_state(request)
    state: ServiceState = request.app.state.service_state
    return StateResponse(state=state)

@router.post("/stop")
async def stop(request: Request):
    pow_manager: PowManager = request.app.state.pow_manager
    inference_manager: InferenceManager = request.app.state.inference_manager
    train_manager: TrainManager = request.app.state.train_manager

    if pow_manager.is_running():
        pow_manager.stop()
    if inference_manager.is_running():
        inference_manager.stop()
    if train_manager.is_running():
        train_manager.stop()

    return {"status": "OK"}
