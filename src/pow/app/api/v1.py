from contextlib import asynccontextmanager

from fastapi import APIRouter, Body, Request, HTTPException

from pow.app.api.server import (
    PowInitRequestUrl,
    _initiate,
    _start_generation,
    _start_validation,
    _status,
    _stop,
    _validate,
)
from pow.compute.compute import ProofBatch
from pow.app.server import GpuManager
from pow.app.api.server import InferenceInitRequest
from pow.utils import create_logger

logger = create_logger(__name__)


@asynccontextmanager
async def lifespan(app):
    app.state.manager: GpuManager = GpuManager()
    yield
    if app.state.manager is not None:
        app.state.manager.stop_pow()
        app.state.manager.stop_vllm()


router = APIRouter(
    tags=["API v1"],
    lifespan=lifespan
)


@router.post("/inference/up")
async def inference_setup(
    request: Request,
    init_request: InferenceInitRequest
):
    manager: GpuManager = request.app.state.manager
    if manager.is_vllm_running():
        logger.info("VLLM is already running")
        manager.stop_vllm()

    manager.init_vllm(init_request)
    manager.start_vllm()
    return {
        "status": "OK"
    }


@router.post("/pow/init")
async def init_generate(
    request: Request,
    init_request: PowInitRequestUrl
):
    manager: GpuManager = request.app.state.manager
    manager.switch_to_pow(init_request)
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post("/pow/init/generate")
async def init_generate(
    request: Request,
    init_request: PowInitRequestUrl
):
    manager: GpuManager = request.app.state.manager
    if not manager.is_pow_running():
        manager.switch_to_pow(init_request)

    if manager.init_request != init_request:
        manager.switch_to_pow(init_request)

    manager.pow_controller.start_generate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post("/pow/init/validate")
async def init_generate(
    request: Request,
    init_request: PowInitRequestUrl
):
    manager: GpuManager = request.app.state.manager
    if not manager.is_pow_running():
        manager.switch_to_pow(init_request)

    if manager.init_request != init_request:
        manager.switch_to_pow(init_request)

    manager.pow_controller.start_validate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post("/pow/phase/generate")
async def init_generate(request: Request):
    manager: GpuManager = request.app.state.manager
    if not manager.is_pow_running():
        raise HTTPException(
            status_code=400,
            detail="PoW is not running"
        )
    manager.pow_controller.start_generate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post("/pow/phase/validate")
async def init_generate(request: Request):
    manager: GpuManager = request.app.state.manager
    if not manager.is_pow_running():
        raise HTTPException(
            status_code=400,
            detail="PoW is not running"
        )
    manager.pow_controller.start_validate()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }


@router.post("/pow/validate")
async def validate(
    request: Request,
    proof_batch: ProofBatch = Body(...)
):
    manager: GpuManager = request.app.state.manager
    if not manager.is_pow_running():
        raise HTTPException(
            status_code=400,
            detail="PoW is not running"
        )

    manager.pow_controller.to_validate(proof_batch)
    manager.sender.in_validation_queue.put(proof_batch)


@router.get("/pow/status")
async def status(request: Request):
    manager: GpuManager = request.app.state.manager
    return manager.get_pow_status()


@router.post("/pow/stop")
async def stop(request: Request):
    manager: GpuManager = request.app.state.manager
    if not manager.is_pow_running():
        return {
            "status": "OK",
            "pow_status": "PoW is not running"
        }
    manager.stop_pow()
    return {
        "status": "OK",
        "pow_status": manager.get_pow_status()
    }
