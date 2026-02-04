import os
import subprocess
import time
import requests
import gc
import torch
import shutil
import shlex
import psutil
import signal
from pathlib import Path
from typing import Optional, List
from abc import abstractmethod

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

    @staticmethod
    def _fix_flashinfer_cache_if_locked():
        hf_home = Path(os.getenv("HF_HOME", "/root/.cache"))
        flashinfer_cache_dir = hf_home / "flashinfer"
        if not flashinfer_cache_dir.exists():
            return
        
        has_lock_files = any(
            file.suffix == ".lock"
            for file in flashinfer_cache_dir.rglob("*")
            if file.is_file()
        )
        
        if has_lock_files:
            logger.warning("Found .lock files in flashinfer cache, deleting cache directory: %s", flashinfer_cache_dir)
            shutil.rmtree(flashinfer_cache_dir, ignore_errors=True)
            logger.info("Flashinfer cache deleted successfully")
        
    def _verify_and_fix_env(self):
        self._fix_flashinfer_cache_if_locked()

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

        self._verify_and_fix_env()

        backend_ports = []
        for i in range(instances):
            sleep_time = 5 * i
            port = self.VLLM_PORT + i + 1
            backend_ports.append(port)
            vllm_command = [
                self.vllm_python_path,
                "-m", "vllm.entrypoints.openai.api_server",
                "--model", self.model,
                "--dtype", self.dtype,
                "--port", str(port),
                "--host", self.VLLM_HOST
            ] + self.additional_args

            vllm_command_str = " ".join(shlex.quote(arg) for arg in vllm_command)
            
            command = ["sh", "-c", f"sleep {sleep_time} && exec {vllm_command_str}"]

            env = os.environ.copy()
            env["VLLM_USE_V1"] = "0"

            start_gpu = i * gpus_per_instance
            if total_gpus > 0:
                gpu_ids = list(range(start_gpu, start_gpu + gpus_per_instance))
                env["CUDA_VISIBLE_DEVICES"] = ",".join(str(g) for g in gpu_ids)

            logger.info("Starting vLLM instance %d on port %d with GPUs %s (sleep: %ds)", i, port, env.get("CUDA_VISIBLE_DEVICES", "all"), sleep_time)
            process = subprocess.Popen(
                command,
                env=env,
                start_new_session=True,
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
            pid = p.pid
            try:
                parent = psutil.Process(pid)
                processes = parent.children(recursive=True) + [parent]
                
                try:
                    logger.info("Sending SIGINT to vLLM process group (PGID %d) for graceful shutdown...", pid)
                    os.killpg(pid, signal.SIGINT)
                except Exception:
                    logger.exception("Failed to send SIGINT to PGID %d; falling back to individual SIGTERM.", pid)
                    for proc in processes:
                        try:
                            proc.terminate()
                        except psutil.NoSuchProcess:
                            pass
                
                logger.info("Waiting for %d processes to terminate...", len(processes))
                _, alive = psutil.wait_procs(processes, timeout=TERMINATION_TIMEOUT)
                
                for proc in alive:
                    try:
                        proc.kill()
                    except psutil.NoSuchProcess:
                        pass
                
            except psutil.NoSuchProcess:
                logger.debug("Process %d already terminated.", pid)

        for p in self.processes:
            try:
                p.wait(timeout=TERMINATION_TIMEOUT)
            except subprocess.TimeoutExpired:
                logger.warning("Termination timed out for PID %d; already handled via psutil kill.", p.pid)
                p.wait()  # Still reap the process

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
