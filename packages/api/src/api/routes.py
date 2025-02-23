from fastapi import APIRouter, Request

from api.service_management import ServiceState
from pow.service.manager import PowManager
from inference.manager import InferenceManager
from zeroband.service.manager import TrainManager
from pow.utils import create_logger

logger = create_logger(__name__)

router = APIRouter(
    tags=["API v1"],
)

@router.post("/mlnode/state")
async def state(request: Request):
    state: ServiceState = request.app.state.service_state
    return {'state': state.value}

@router.post("/mlnode/stop")
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
