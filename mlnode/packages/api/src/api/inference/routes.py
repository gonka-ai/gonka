from fastapi import (
    APIRouter,
    Request,
    HTTPException,
    Security,
)

from api.inference.manager import (
    InferenceManager,
    InferenceInitRequest,
)
from pow.service.auth import verify_signature

from common.logger import create_logger

logger = create_logger(__name__)

router = APIRouter()


@router.post("/inference/up")
async def inference_setup(
    request: Request,
    init_request: InferenceInitRequest,
    _auth=Security(verify_signature),
):
    """Start inference and wait for it to be ready. Returns error if already running or starting."""
    manager: InferenceManager = request.app.state.inference_manager
    
    if manager.is_running():
        raise HTTPException(
            status_code=409,
            detail="VLLM is already running. Use /inference/down to stop it first."
        )
    
    if manager.is_starting():
        raise HTTPException(
            status_code=409,
            detail="VLLM startup is already in progress. Use /inference/up/status to check status."
        )

    try:
        manager.start_async(init_request)
        await manager._startup_task
        return {"status": "OK"}
    except ValueError as e:
        raise HTTPException(status_code=409, detail=str(e))
    except Exception as e:
        logger.error(f"Failed to start VLLM: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.post("/inference/up/async")
async def inference_setup_async(
    request: Request,
    init_request: InferenceInitRequest,
    _auth=Security(verify_signature),
):
    """Start inference asynchronously in the background. Returns error if already running or starting."""
    manager: InferenceManager = request.app.state.inference_manager
    
    if manager.is_running():
        raise HTTPException(
            status_code=409,
            detail="VLLM is already running. Use /inference/down to stop it first."
        )
    
    if manager.is_starting():
        raise HTTPException(
            status_code=409,
            detail="VLLM startup is already in progress. Use /inference/up/status to check status."
        )
    
    try:
        manager.start_async(init_request)
        return {
            "status": "starting",
            "message": "Inference startup initiated in background"
        }
    except ValueError as e:
        raise HTTPException(status_code=409, detail=str(e))
    except Exception as e:
        logger.error(f"Failed to start async VLLM: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/inference/up/status")
async def inference_startup_status(
    request: Request,
    _auth=Security(verify_signature),
):
    """Check the status of async inference startup."""
    manager: InferenceManager = request.app.state.inference_manager
    
    status = manager.get_startup_status()
    return status


@router.post("/inference/down")
async def inference_down(
    request: Request,
    _auth=Security(verify_signature),
):
    manager: InferenceManager = request.app.state.inference_manager
    # Use async stop in async context to avoid blocking event loop
    await manager._async_stop()
    return {
        "status": "OK"
    }
