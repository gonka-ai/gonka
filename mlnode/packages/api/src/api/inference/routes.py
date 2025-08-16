from fastapi import APIRouter, Request

from api.inference.manager import InferenceManager, InferenceInitRequest

from common.logger import create_logger

logger = create_logger(__name__)

router = APIRouter()


@router.post("/inference/up")
def inference_setup(
    request: Request,
    init_request: InferenceInitRequest
):
    manager: InferenceManager = request.app.state.inference_manager
    
    # Stop if already running
    if manager.is_running():
        logger.info("VLLM is already running, stopping first")
        manager.stop()

    # Initialize and start
    try:
        manager.init_vllm(init_request)
        manager.start()
        return {"status": "OK"}
    except Exception as e:
        logger.error(f"Failed to start VLLM: {e}")
        return {"status": "ERROR", "message": str(e)}

@router.post("/inference/down")
async def inference_down(
    request: Request
):
    manager: InferenceManager = request.app.state.inference_manager
    manager.stop()
    return {
        "status": "OK"
    }
