from typing import List, Optional

from pow.compute.controller import ParallelController
from pow.utils import create_logger
from pow.app.sender import Sender
from pow.app.api.server import PowInitRequest, InferenceInitRequest, PowState
from pow.compute.utils import Phase
from pow.app.vllm.runner import VLLMRunner


logger = create_logger(__name__)


class GpuManager:
    def __init__(self):
        self.pow_controller: Optional[ParallelController] = None
        self.pow_sender: Optional[Sender] = None
        self.init_request: Optional[PowInitRequest] = None

        self.vllm_runner: Optional[VLLMRunner] = None

    def switch_to_pow(
        self,
        init_request: PowInitRequest
    ):
        if self.pow_controller is not None:
            logger.info("Stopping PoW controller")
            self.stop_pow()

        if self.is_vllm_running():
            logger.info("Stopping VLLM runner")
            self.stop_vllm()
        
        self.init_pow(init_request)
        self.start_pow()

    def init_pow(
        self,
        init_request: PowInitRequest
    ):
        self.init_request = init_request
        self.pow_controller = ParallelController(
            params=init_request.params,
            block_hash=init_request.block_hash,
            block_height=init_request.block_height,
            public_key=init_request.public_key,
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

    def start_pow(self):
        if self.pow_controller is None:
            raise Exception("PoW not initialized")
        
        if self.pow_controller.is_running():
            raise Exception("PoW is already running")

        if self.is_vllm_running():
            raise Exception("VLLMRunner is running")

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

    def init_vllm(
        self,
        init_request: InferenceInitRequest
    ):
        if self.is_vllm_running():
            raise Exception("VLLMRunner already initialized")
        
        self.vllm_runner = VLLMRunner(
            model=init_request.model,
            dtype=init_request.dtype,
            additional_args=init_request.additional_args,
        )

    def start_vllm(self):
        if self.vllm_runner is None:
            raise Exception("VLLMRunner not initialized")

        if self.is_vllm_running():
            raise Exception("VLLMRunner is running")

        if self.is_pow_running():
            raise Exception("PoW is running")

        self.vllm_runner.start()

    def stop_vllm(self):
        self.vllm_runner.stop()

    def stop_pow(self):
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

    def is_pow_running(self) -> bool:
        return self.pow_controller is not None and self.pow_controller.is_running()

    def is_vllm_running(self) -> bool:
        return self.vllm_runner is not None and self.vllm_runner.is_running()
