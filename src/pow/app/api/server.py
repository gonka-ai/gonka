from enum import Enum
from typing import List

from fastapi import HTTPException
from pydantic import BaseModel

from pow.compute.compute import ProofBatch
from pow.compute.controller import ParallelController
from pow.compute.utils import Phase
from pow.models.utils import Params
from pow.utils import create_logger
from pow.app.sender import Sender


logger = create_logger(__name__)


class AppState(Enum):
    IDLE = "IDLE"
    NO_CONTROLLER = "NOT_LOADED"
    LOADING = "LOADING"
    GENERATING = "GENERATING"
    VALIDATING = "VALIDATING"
    STOPPED = "STOPPED"
    MIXED = "MIXED"


class InitRequest(BaseModel):
    chain_hash: str
    public_key: str
    batch_size: int
    r_target: float
    params: Params = Params()


class InitRequestWithUrl(InitRequest):
    url: str


def create_sender(
    app,
    url: str,
    controller: ParallelController,
):
    sender = Sender(
        url=url,
        generation_queue=controller.generated_batch_queue,
        validation_queue=controller.validated_batch_queue,
        phase=controller.phase,
        r_target=controller.r_target,
    )
    return sender


def _initiate(
    app,
    init_request: InitRequest
):
    if app.state.controller is not None:
        raise HTTPException(
            status_code=400,
            detail="Controller already initialized"
        )
    app.state.controller = ParallelController(
        params=init_request.params,
        chain_hash=init_request.chain_hash,
        public_key=init_request.public_key,
        batch_size=init_request.batch_size,
        r_target=init_request.r_target,
        devices=None,
    )
    sender = create_sender(app, init_request.url, app.state.controller)
    app.state.sender = sender
    logger.info("Starting controller with params: %s", init_request)

    controller = app.state.controller
    controller.start()
    sender.start()

    app.state.init_request = init_request
    return {
        "status": AppState.LOADING
    }

def _stop(app):
    if app.state.controller is None:
        raise HTTPException(
            status_code=400,
            detail="Controller not initialized"
        )
    controller = app.state.controller
    controller.stop()

    sender: Sender = app.state.sender
    sender.stop()
    sender.join(timeout=5)  # Wait for up to 5 seconds

    if sender.is_alive():
        logger.warning("Sender process did not stop within the timeout period")

    app.state.controller = None
    app.state.sender = None
    return {
        "status": AppState.STOPPED
    }

def _start_generation(app):
    controller = app.state.controller
    if controller is None:
        raise HTTPException(
            status_code=400,
            detail="Controller not initialized"
        )
    controller.start_generate()
    response = {
        "status": AppState.GENERATING
    }

    if not controller.is_model_initialized():
        response["is_model_initialized"] = False
        response["details"] = "Model is still loading"

    return response

def _start_validation(app):
    controller = app.state.controller
    if controller is None:
        raise HTTPException(
            status_code=400,
            detail="Controller not initialized"
        )
    controller.start_validate()
    response = {
        "status": AppState.VALIDATING
    }
    if not controller.is_model_initialized():
        response["is_model_initialized"] = False
        response["details"] = "Model is still loading"
    return response


def _status(app):
    state = None
    controller = app.state.controller
    if controller is None:
        return {"status": AppState.NO_CONTROLLER}

    response = {}
    phase = controller.phase.value
    if phase == Phase.IDLE:
        state = AppState.IDLE
    elif phase == Phase.GENERATE:
        state = AppState.GENERATING
    elif phase == Phase.VALIDATE:
        state = AppState.VALIDATING
    response = {"status": state}
    if controller is not None and not controller.is_model_initialized():
        response["is_model_initialized"] = False
        response["details"] = "Model is still loading"
    return response


def _get_generated(app) -> ProofBatch:
    controller = app.state.controller
    if controller is None:
        raise HTTPException(status_code=400, detail="Controller not initialized")
    phase = controller.phase.value
    if phase != Phase.GENERATE:
        raise HTTPException(
            status_code=400,
            detail="Controller not initialized"
        )

    return ProofBatch.merge(controller.get_generated())

def _validate(app, proof_batch: ProofBatch):
    controller = app.state.controller
    sender = app.state.sender
    if controller is None or sender is None:
        raise HTTPException(
            status_code=400,
            detail="Controller not initialized"
        )

    controller.to_validate(proof_batch)
    sender.in_validation_queue.put(proof_batch)


def _get_validated(app) -> List[ProofBatch]:
    controller = app.state.controller
    if controller is None:
        raise HTTPException(
            status_code=400,
            detail="Controller not initialized"
        )
    return [
        ProofBatch.merge([x]) for x in controller.get_validated()
    ]
