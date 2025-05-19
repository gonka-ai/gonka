import os
import subprocess
import time
import requests
import gc
import torch
from typing import Optional, List
from abc import ABC, abstractmethod

from common.logger import create_logger
from common.trackable_task import ITrackableTask


TERMINATION_TIMEOUT = 20
WAIT_FOR_SERVER_TIMEOUT = 1200
WAIT_FOR_SERVER_CHECK_INTERVAL = 3

logger = create_logger(__name__)


class IVLLMRunner(ITrackableTask):
    @abstractmethod
    def is_available(self) -> bool:
        pass

    @abstractmethod
    def is_running(self) -> bool:
        pass

    def is_alive(self) -> bool:
        return self.is_available()


class VLLMRunner(IVLLMRunner):
    VLLM_PYTHON_PATH = "/opt/venv/bin/python3"
    VLLM_PORT = os.getenv("INFERENCE_PORT", 5000)
    VLLM_HOST = "0.0.0.0"

    def __init__(
        self,
        model: str,
        dtype: str = "auto",
        additional_args: List[str] = None,
    ):
        self.vllm_python_path = os.getenv("VLLM_PYTHON_PATH", self.VLLM_PYTHON_PATH)
        self.model = model
        self.dtype = dtype
        self.additional_args = additional_args or []
        self.process: Optional[subprocess.Popen] = None

    def start(self):
        if self.process:
            raise RuntimeError("VLLMRunner is already running")

        command = [
            self.vllm_python_path,
            "-m", "vllm.entrypoints.openai.api_server",
            "--model", self.model,
            "--dtype", self.dtype,
            "--port", str(self.VLLM_PORT),
            "--host", self.VLLM_HOST
        ] + self.additional_args

        env = os.environ.copy()
        env["VLLM_USE_V1"] = "0"

        self.process = subprocess.Popen(
            command,
            env=env,
        )

        if not self._wait_for_server():
            raise RuntimeError(f"vLLM failed to start within the expected timeout: {self.get_error_if_exist()}")

        logger.info("vLLM is up and running.")

    def stop(self):
        if not self.process:
            logger.warning("VLLMRunner stop called but no process is running.")
            return

        logger.info("Stopping vLLM process...")
        self.process.terminate()

        try:
            self.process.wait(timeout=TERMINATION_TIMEOUT)
        except subprocess.TimeoutExpired:
            logger.warning("Termination timed out; forcefully killing vLLM process.")
            self.process.kill()
            self.process.wait()

        self.process = None
        self._cleanup_gpu()
        logger.info("vLLM process stopped.")

    def _cleanup_gpu(self):
        logger.debug("Cleaning GPU memory...")
        torch.cuda.empty_cache()
        gc.collect()

    def _wait_for_server(self) -> bool:
        start_time = time.time()
        while time.time() - start_time < WAIT_FOR_SERVER_TIMEOUT:
            if not self.is_running():
                raise RuntimeError(f"vLLM process exited prematurely: {self.get_error_if_exist()}")

            if self.is_available():
                return True

            time.sleep(WAIT_FOR_SERVER_CHECK_INTERVAL)

        logger.error("vLLM server did not become available within timeout.")
        return False

    def is_running(self) -> bool:
        return self.process is not None and self.process.poll() is None

    def is_available(self) -> bool:
        if not self.is_running():
            return False
        try:
            resp = requests.get(f"http://{self.VLLM_HOST}:{self.VLLM_PORT}/v1/models")
            return resp.status_code == 200
        except requests.ConnectionError:
            return False

    def get_error_if_exist(self) -> Optional[str]:
        if self.process and self.process.stderr:
            return self.process.stderr.read().strip()
        return None
