import os

from fastapi import APIRouter, Body, Request, HTTPException
from starlette.background import BackgroundTask
from starlette.responses import JSONResponse

from pow.service.manager import PowInitRequestUrl, PowManager
from pow.compute.compute import ProofBatch
from pow.compute.gpu_group import NotEnoughGPUResources
from common.logger import create_logger

logger = create_logger(__name__)

API_PREFIX = "/api/v1"

router = APIRouter(
    tags=["API v1"],
)

@router.post(
    "/pow/init",
    status_code=200,
)
async def init(
    request: Request,
    init_request: PowInitRequestUrl
):
    manager: PowManager = request.app.state.pow_manager
    try:
        await manager.switch_to_pow_async(init_request)
    except NotEnoughGPUResources as e:
        logger.critical(f"GPU resources unavailable: {e}. Shutting down.")
        return JSONResponse(
            status_code=503,
            content={"detail": str(e)},
            background=BackgroundTask(os._exit, 1),
        )
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post(
    "/pow/init/generate",
    status_code=200,
)
async def init_generate(
    request: Request,
    init_request: PowInitRequestUrl
):
    if init_request.node_id == -1 or init_request.node_count == -1:
        raise HTTPException(
            status_code=400,
            detail="Node ID and node count must be set"
        )
    manager: PowManager = request.app.state.pow_manager
    try:
        if not manager.is_running():
            await manager.switch_to_pow_async(init_request)

        if manager.init_request != init_request:
            await manager.switch_to_pow_async(init_request)
    except NotEnoughGPUResources as e:
        logger.critical(f"GPU resources unavailable: {e}. Shutting down.")
        return JSONResponse(
            status_code=503,
            content={"detail": str(e)},
            background=BackgroundTask(os._exit, 1),
        )

    manager.pow_controller.start_generate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post(
    "/pow/init/validate",
    status_code=200,
)
async def init_validate(
    request: Request,
    init_request: PowInitRequestUrl
):
    manager: PowManager = request.app.state.pow_manager
    try:
        if not manager.is_running():
            await manager.switch_to_pow_async(init_request)

        if manager.init_request != init_request:
            await manager.switch_to_pow_async(init_request)
    except NotEnoughGPUResources as e:
        logger.critical(f"GPU resources unavailable: {e}. Shutting down.")
        return JSONResponse(
            status_code=503,
            content={"detail": str(e)},
            background=BackgroundTask(os._exit, 1),
        )

    manager.pow_controller.start_validate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post(
    "/pow/phase/generate",
    status_code=200,
)
async def start_generate(request: Request):
    manager: PowManager = request.app.state.pow_manager
    if manager.init_request.node_id == -1 or manager.init_request.node_count == -1:
        raise HTTPException(
            status_code=400,
            detail="Node ID and node count must be set to start generating"
        )
    if not manager.is_running():
        raise HTTPException(
            status_code=400,
            detail="PoW is not running"
        )
    manager.pow_controller.start_generate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post(
    "/pow/phase/validate",
    status_code=200,
)
async def start_validate(request: Request):
    manager: PowManager = request.app.state.pow_manager
    if not manager.is_running():
        raise HTTPException(
            status_code=400,
            detail="PoW is not running"
        )
    manager.pow_controller.start_validate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post(
    "/pow/validate",
    status_code=200,
)
async def validate(
    request: Request,
    proof_batch: ProofBatch = Body(...)
):
    manager: PowManager = request.app.state.pow_manager
    if not manager.is_running():
        raise HTTPException(
            status_code=400,
            detail="PoW is not running"
        )

    manager.pow_controller.to_validate(proof_batch)
    manager.pow_sender.in_validation_queue.put(proof_batch)


@router.get(
    "/pow/status",
    status_code=200,
)
async def status(request: Request):
    manager: PowManager = request.app.state.pow_manager
    return manager.get_pow_status()


@router.post(
    "/pow/stop",
    status_code=200,
)
async def stop(request: Request):
    manager: PowManager = request.app.state.pow_manager
    if not manager.is_running():
        return {
            "status": "OK",
            "pow_status": "PoW is not running"
        }
    manager.stop()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }
