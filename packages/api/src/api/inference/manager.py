from typing import Optional, List, Type
from pydantic import BaseModel

from api.inference.vllm.runner import (
    IVLLMRunner,
    VLLMRunner,
)

from common.logger import create_logger

logger = create_logger(__name__)


class InferenceInitRequest(BaseModel):
    model: str
    dtype: str
    additional_args: List[str] = []


class InferenceManager:
    def __init__(
        self,
        runner_class: Type[IVLLMRunner] = VLLMRunner
    ):
        self.vllm_runner: Optional[IVLLMRunner] = None
        self.runner_class = runner_class

    def init_vllm(
        self,
        init_request: InferenceInitRequest
    ):
        print(f"init_vllm: {self.is_running()}")
        if self.is_running():
            raise Exception("VLLMRunner is already running. Stop it first.")
        
        self.vllm_runner = self.runner_class(
            model=init_request.model,
            dtype=init_request.dtype,
            additional_args=init_request.additional_args,
        )

    def start(self):
        if self.vllm_runner is None:
            raise Exception("VLLMRunner not initialized")

        if self.is_running():
            raise Exception("VLLMRunner is running")

        self.vllm_runner.start()

    def stop(self):
        self.vllm_runner.stop()

    def is_running(self) -> bool:
        return self.vllm_runner is not None and self.vllm_runner.is_running()
