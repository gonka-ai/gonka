# src/pow/app/api_v0.py

from typing import List

from fastapi import APIRouter, Body, Request

from pow.app.api.server import (
    InitRequest,
    _get_generated,
    _get_validated,
    _initiate,
    _start_generation,
    _start_validation,
    _status,
    _stop,
    _validate,
)
from pow.compute.compute import ProofBatch


router = APIRouter(
    tags=["API v0"]
)


@router.post("/initiate")
async def initiate(
    request: Request,
    init_request: InitRequest
):
    return _initiate(
        request.app,
        init_request
    )


@router.post("/stop")
async def stop(request: Request):
    return _stop(request.app)


@router.post("/start-generation")
async def start_generation(request: Request):
    return _start_generation(request.app)


@router.post("/start-validation")
async def start_validation(request: Request):
    return _start_validation(request.app)


@router.get("/status")
async def status(request: Request):
    return _status(request.app)


@router.get("/generated")
async def get_generated(request: Request) -> ProofBatch:
    return _get_generated(request.app)


@router.post("/validate")
async def validate(
    request: Request,
    proof_batch: ProofBatch = Body(...)
):
    return _validate(request.app, proof_batch)


@router.get("/validated")
async def get_validated(request: Request) -> List[ProofBatch]:
    return _get_validated(request.app)
