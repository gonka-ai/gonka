from fastapi import APIRouter, Request, HTTPException, Security

from zeroband.service.manager import TrainManager
from pow.service.auth import verify_signature

API_PREFIX = "/api/v1"
router = APIRouter(tags=["Training API"])

@router.post("/train/start")
async def start(
    request: Request,
    training_dict: dict,
    _auth=Security(verify_signature),
):
    manager: TrainManager = request.app.state.train_manager
    if manager.is_running():
        raise HTTPException(status_code=400, detail="Training is already running")
    await manager.start_async(training_dict)
    return {"status": "Training started"}

@router.post("/train/stop")
async def stop(
    request: Request,
    _auth=Security(verify_signature),
):
    manager: TrainManager = request.app.state.train_manager
    if not manager.is_running():
        raise HTTPException(status_code=400, detail="Training is not running")
    manager.stop()
    return {"status": "Training stopped"}

@router.get("/train/status")
async def status(
    request: Request,
    _auth=Security(verify_signature),
):
    manager: TrainManager = request.app.state.train_manager
    return {"status": manager.is_running()}
