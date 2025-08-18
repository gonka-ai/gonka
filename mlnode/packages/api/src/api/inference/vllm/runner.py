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
from api.proxy import setup_vllm_proxy


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

    @abstractmethod
    def start(self) -> None:
        pass

    @abstractmethod
    def stop(self) -> None:
        pass

    def is_alive(self) -> bool:
        return self.is_available()


class VLLMRunner(IVLLMRunner):
    VLLM_PYTHON_PATH = "/usr/bin/python3.12"
    VLLM_PORT = int(os.getenv("INFERENCE_PORT", 5000))
    VLLM_HOST = "0.0.0.0"

    MAX_INSTANCES = int(os.getenv("INFERENCE_MAX_INSTANCES", 128))

    def __init__(
        self,
        model: str,
        dtype: str = "auto",
        additional_args: Optional[List[str]] = None,
    ):
        self.vllm_python_path = os.getenv("VLLM_PYTHON_PATH", self.VLLM_PYTHON_PATH)
        self.model = model
        self.dtype = dtype
        self.additional_args = additional_args or []
        self.processes: List[subprocess.Popen] = []

    def _get_arg_value(self, name: str, default: int = 1) -> int:
        if name in self.additional_args:
            try:
                idx = self.additional_args.index(name)
                return int(self.additional_args[idx + 1])
            except (ValueError, IndexError):
                pass
        return default

    def start(self):
        if self.processes:
            raise RuntimeError("VLLMRunner is already running")

        tp_size = self._get_arg_value("--tensor-parallel-size", default=1)
        pp_size = self._get_arg_value("--pipeline-parallel-size", default=1)
        gpus_per_instance = tp_size * pp_size
        logger.info("gpus per instance: %d (tp_size: %d, pp_size: %d)", gpus_per_instance, tp_size, pp_size)
        total_gpus = max(torch.cuda.device_count(), 1)
        logger.info("total available gpus: %d", total_gpus)
        instances = min(self.MAX_INSTANCES, max(1, total_gpus // gpus_per_instance))
        logger.info("instances to start: %d", instances)

        backend_ports = []
        for i in range(instances):
            port = self.VLLM_PORT + i + 1
            backend_ports.append(port)
            command = [
                self.vllm_python_path,
                "-m", "vllm.entrypoints.openai.api_server",
                "--model", self.model,
                "--dtype", self.dtype,
                "--port", str(port),
                "--host", self.VLLM_HOST
            ] + self.additional_args

            env = os.environ.copy()
            env["VLLM_USE_V1"] = "0"

            start_gpu = i * gpus_per_instance
            if total_gpus > 0:
                gpu_ids = list(range(start_gpu, start_gpu + gpus_per_instance))
                env["CUDA_VISIBLE_DEVICES"] = ",".join(str(g) for g in gpu_ids)

            logger.info("Starting vLLM instance %d on port %d with GPUs %s", i, port, env.get("CUDA_VISIBLE_DEVICES", "all"))
            process = subprocess.Popen(
                command,
                env=env,
            )
            self.processes.append(process)

        # Setup the integrated proxy instead of starting separate process
        logger.info("Setting up proxy with backend ports: %s", backend_ports)
        setup_vllm_proxy(backend_ports)
        logger.info("vLLM proxy integrated with main API server")

        if not self._wait_for_server():
            raise RuntimeError(f"vLLM failed to start within the expected timeout: {self.get_error_if_exist()}")

        logger.info("vLLM is up and running with %d instance(s).", instances)

    def stop(self):
        if not self.processes:
            logger.warning("VLLMRunner stop called but no process is running.")
            return

        logger.info("Stopping vLLM processes...")
        for p in self.processes:
            p.terminate()

        for p in self.processes:
            try:
                p.wait(timeout=TERMINATION_TIMEOUT)
            except subprocess.TimeoutExpired:
                logger.warning("Termination timed out; forcefully killing vLLM process.")
                p.kill()
                p.wait()

        self.processes = []
        self._cleanup_gpu()
        logger.info("vLLM processes stopped.")

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
        return len(self.processes) > 0 and all(p.poll() is None for p in self.processes)

    def is_available(self) -> bool:
        if not self.is_running():
            return False
        try:
            # Check if any backend is available
            for port in range(self.VLLM_PORT + 1, self.VLLM_PORT + len(self.processes) + 1):
                resp = requests.get(f"http://{self.VLLM_HOST}:{port}/health", timeout=2)
                if resp.status_code == 200:
                    return True
            return False
        except (requests.ConnectionError, requests.Timeout):
            return False

    def get_error_if_exist(self) -> Optional[str]:
        for p in self.processes:
            if p.stderr:
                err = p.stderr.read().strip()
                if err:
                    return err
        return None
