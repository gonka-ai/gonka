from typing import Optional, List
from pydantic import BaseModel

from inference.vllm_runner import VLLMRunner

from pow.utils import create_logger

logger = create_logger(__name__)


class InferenceInitRequest(BaseModel):
    model: str
    dtype: str
    additional_args: List[str] = []


class InferenceManager:
    def __init__(self):
        self.vllm_runner: Optional[VLLMRunner] = None

    def init_vllm(
        self,
        init_request: InferenceInitRequest
    ):
        if self.is_running():
            raise Exception("VLLMRunner already initialized")
        
        self.vllm_runner = VLLMRunner(
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
