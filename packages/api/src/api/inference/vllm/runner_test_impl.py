from typing import List, Optional

from api.inference.vllm.runner import IVLLMRunner


class VLLMRunnerTestImpl(IVLLMRunner):
    def __init__(
        self,
        model: str,
        dtype: str = "auto",
        additional_args: Optional[List[str]] = None
    ):
        self._running = False
        self._model = model
        self._dtype = dtype
        self._additional_args = additional_args

    def start(self):
        self._running = True

    def stop(self):
        self._running = False

    def is_running(self) -> bool:
        return self._running

    def is_available(self) -> bool:
        return self._running
