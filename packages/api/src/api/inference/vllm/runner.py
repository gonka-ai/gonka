import os
import subprocess
import time
import requests
from typing import Optional, List
from abc import ABC, abstractmethod

from common.logger import create_logger

TERMINATION_TIMEOUT = 20
WAIT_FOR_SERVER_TIMEOUT = 1200
WAIT_FOR_SERVER_CHECK_INTERVAL = 3

logger = create_logger(__name__)


class IVLLMRunner(ABC):
    @abstractmethod
    def start(self):
        pass

    @abstractmethod
    def stop(self):
        pass

    @abstractmethod
    def is_running(self) -> bool:
        pass

    @abstractmethod
    def is_available(self) -> bool:
        pass



class VLLMRunner(IVLLMRunner):
    VLLM_PYTHON_PATH = "/opt/venv/bin/python3"
    VLLM_PORT = 5000
    VLLM_HOST = "0.0.0.0"

    def __init__(
        self,
        model: str,
        dtype: str = "auto",
        additional_args: List[str] = None,
    ):
        self.vllm_python_path = os.getenv(
            "VLLM_PYTHON_PATH",
            self.VLLM_PYTHON_PATH
        )
        self.model = model
        self.dtype = dtype
        self.additional_args = additional_args or []
        
        self.process: Optional[subprocess.Popen] = None

    def start(self):
        if self.process is not None:
            raise RuntimeError("VLLMRunner is already running")

        env = os.environ.copy()
        env["VLLM_USE_V1"] = "0"
        
        command = [
            self.vllm_python_path,
            "-m", "vllm.entrypoints.openai.api_server",
            "--model", self.model,
            "--dtype", self.dtype,
            "--port", str(self.VLLM_PORT),
            "--host", self.VLLM_HOST
        ] + self.additional_args

        self.process = subprocess.Popen(
            command,
            env=env,
        )

    def stop(self):
        if self.process is None:
            raise RuntimeError("VLLMRunner is not running")

        self.process.terminate()
        self.process.wait(timeout=TERMINATION_TIMEOUT)
        if self.process.poll() is None:
            self.process.kill()
        self.process = None

    def _wait_for_server(
        self,
        timeout: int = WAIT_FOR_SERVER_TIMEOUT,
        check_interval: int = WAIT_FOR_SERVER_CHECK_INTERVAL
    ):
        start_time = time.time()
        while time.time() - start_time < timeout:
            try:
                requests.get(f"http://{self.VLLM_HOST}:{self.VLLM_PORT}/v1/models")
                return
            except requests.ConnectionError:
                time.sleep(check_interval)
        raise TimeoutError("Server did not start within the specified timeout")

    def _check_process(self):
        if self.process is not None:
            return_code = self.process.poll()
            if return_code is not None:
                logger.warning(f"VLLM process has terminated unexpectedly with return code {return_code}")
                self.process = None
                return False
        return True

    def is_running(self):
        if self.process is None:
            return False
        return self._check_process()

    def is_available(self):
        if not self._check_process():
            return False
        try:
            requests.get(f"http://{self.VLLM_HOST}:{self.VLLM_PORT}/v1/models")
            return True
        except requests.ConnectionError:
            return False
