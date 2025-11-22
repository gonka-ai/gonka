import asyncio
from typing import Optional
from enum import Enum
from pydantic import BaseModel

from pow.models.utils import Params
from pow.compute.controller import ParallelController
from common.logger import create_logger
from pow.service.sender import Sender
from pow.compute.utils import Phase
from common.manager import IManager


class PowState(Enum):
    IDLE = "IDLE"
    NO_CONTROLLER = "NOT_LOADED"
    LOADING = "LOADING"
    GENERATING = "GENERATING"
    VALIDATING = "VALIDATING"
    STOPPED = "STOPPED"
    MIXED = "MIXED"


class PowInitRequest(BaseModel):
    node_id: int = -1
    node_count: int = -1
    block_hash: str
    block_height: int
    public_key: str
    batch_size: int
    r_target: float
    fraud_threshold: float
    params: Params = Params()


class PowInitRequestUrl(PowInitRequest):
    url: str


logger = create_logger(__name__)


class PowManager(IManager):
    def __init__(self):
        super().__init__()
        self.pow_controller: Optional[ParallelController] = None
        self.pow_sender: Optional[Sender] = None
        self.init_request: Optional[PowInitRequest] = None

    def switch_to_pow(
        self,
        init_request: PowInitRequest
    ):
        if self.pow_controller is not None:
            logger.info("Stopping PoW controller")
            self.stop()
        
        self.init(init_request)
        self.start()
    
    async def switch_to_pow_async(
        self,
        init_request: PowInitRequest
    ):
        return await asyncio.to_thread(self.switch_to_pow, init_request)

    def init(
        self,
        init_request: PowInitRequest
    ):
        self.init_request = init_request
        self.pow_controller = ParallelController(
            params=init_request.params,
            block_hash=init_request.block_hash,
            block_height=init_request.block_height,
            public_key=init_request.public_key,
            node_id=init_request.node_id,
            node_count=init_request.node_count,
            batch_size=init_request.batch_size,
            r_target=init_request.r_target,
            devices=None,
        )
        self.pow_sender = Sender(
            url=init_request.url,
            generation_queue=self.pow_controller.generated_batch_queue,
            validation_queue=self.pow_controller.validated_batch_queue,
            phase=self.pow_controller.phase,
            r_target=self.pow_controller.r_target,
            fraud_threshold=init_request.fraud_threshold,
        )

    def _start(self):
        if self.pow_controller is None:
            raise Exception("PoW not initialized")
        
        if self.pow_controller.is_running():
            raise Exception("PoW is already running")


        logger.info(f"Starting controller with params: {self.init_request}")
        self.pow_controller.start()
        self.pow_sender.start()

    def get_pow_status(self) -> dict:
        if self.pow_controller is None:
            return {
                "status": PowState.NO_CONTROLLER,
            }
        phase = self.phase_to_state(self.pow_controller.phase.value)
        loading = not self.pow_controller.is_model_initialized()
        if loading and phase == PowState.IDLE:
            phase = PowState.LOADING
        return {
            "status": phase,
            "is_model_initialized": not loading,
        }

    def _stop(self):
        self.pow_controller.stop()
        self.pow_sender.stop()
        self.pow_sender.stop()
        self.pow_sender.join(timeout=5)

        if self.pow_sender.is_alive():
            logger.warning("Sender process did not stop within the timeout period")

        self.pow_controller = None
        self.pow_sender = None
        self.init_request = None

    @staticmethod
    def phase_to_state(phase: Phase) -> PowState:
        if phase == Phase.IDLE:
            return PowState.IDLE
        elif phase == Phase.GENERATE:
            return PowState.GENERATING
        elif phase == Phase.VALIDATE:
            return PowState.VALIDATING
        else:
            return PowState.IDLE

    def is_running(self) -> bool:
        return self.pow_controller is not None and self.pow_controller.is_running()

    def _is_healthy(self) -> bool:
        return self.pow_controller is not None and self.pow_controller.is_alive()
