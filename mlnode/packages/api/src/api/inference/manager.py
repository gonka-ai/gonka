from typing import Optional, List, Type
from pydantic import BaseModel

from api.inference.vllm.runner import (
    IVLLMRunner,
    VLLMRunner,
)

from common.logger import create_logger
from common.manager import IManager

logger = create_logger(__name__)


class InferenceInitRequest(BaseModel):
    model: str
    dtype: str
    additional_args: List[str] = []


class InferenceManager(IManager):
    def __init__(
        self,
        runner_class: Type[IVLLMRunner] = VLLMRunner
    ):
        super().__init__()
        self.vllm_runner: Optional[IVLLMRunner] = None
        self.runner_class = runner_class

    def init_vllm(
        self,
        init_request: InferenceInitRequest
    ):
        if self.is_running():
            raise Exception("VLLMRunner is already running. Stop it first.")
        
        self.vllm_runner = self.runner_class(
            model=init_request.model,
            dtype=init_request.dtype,
            additional_args=init_request.additional_args,
        )

    def _start(self):
        if self.vllm_runner is None:
            raise Exception("VLLMRunner not initialized")
        if self.is_running():
            raise Exception("VLLMRunner is already running")
        self.vllm_runner.start()
        logger.info("VLLMRunner started")

    def _stop(self):
        if self.vllm_runner:
            self.vllm_runner.stop()
            logger.info("VLLMRunner stopped")
        
        # Clear the runner reference
        self.vllm_runner = None

    def is_running(self) -> bool:
        return self.vllm_runner is not None and self.vllm_runner.is_running()

    def _is_healthy(self) -> bool:
        if self.vllm_runner is None:
            return False

        is_alive = self.vllm_runner.is_alive()
        if not is_alive:
            error = self.vllm_runner.get_error_if_exist()
            if error:
                logger.error(f"VLLMRunner is not alive: {error}")

        return is_alive
