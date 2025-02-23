from fastapi import APIRouter, Request

from inference.manager import InferenceInitRequest, InferenceManager

from pow.utils import create_logger

logger = create_logger(__name__)

router = APIRouter()

@router.post("/inference/up")
async def inference_setup(
    request: Request,
    init_request: InferenceInitRequest
):
    manager: InferenceManager = request.app.state.inference_manager
    if manager.is_running():
        logger.info("VLLM is already running")
        manager.stop()

    manager.init_vllm(init_request)
    manager.start()
    return {
        "status": "OK"
    }

@router.post("/inference/down")
async def inference_down(
    request: Request
):
    manager: InferenceManager = request.app.state.inference_manager
    manager.stop()
    return {
        "status": "OK"
    }
