from fastapi import APIRouter, Body, Request

from pow.app.api.server import (
    InitRequestWithUrl,
    _initiate,
    _start_generation,
    _start_validation,
    _status,
    _stop,
    _validate,
)
from pow.compute.compute import ProofBatch


router = APIRouter(
    tags=["API v1"]
)


@router.post("/init")
async def init(
    request: Request,
    init_request: InitRequestWithUrl
):
    return _initiate(
        request.app,
        init_request
    )


@router.post("/init-generate")
async def init_generate(
    request: Request,
    init_request: InitRequestWithUrl
):
    if request.app.state.controller is None:
        _initiate(
            request.app,
            init_request
        )
    return _start_generation(request.app)


@router.post("/init-validate")
async def init_validate(
    request: Request,
    init_request: InitRequestWithUrl
):
    if request.app.state.controller is None:
        _initiate(
            request.app,
            init_request
        )
    return _start_validation(request.app)


@router.post("/validate")
async def validate(
    request: Request,
    proof_batch: ProofBatch = Body(...)
):
    return _validate(request.app, proof_batch)


@router.post("/start-generation")
async def start_generation(request: Request):
    return _start_generation(request.app)


@router.post("/start-validation")
async def start_validation(request: Request):
    return _start_validation(request.app)


@router.get("/status")
async def status(request: Request):
    return _status(request.app)


@router.post("/stop")
async def stop(request: Request):
    return _stop(request.app)
