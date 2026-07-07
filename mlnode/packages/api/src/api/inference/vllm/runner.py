import os
import re
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

HANG_CONSECUTIVE = 3

# (metric suffix, cross-series reduce). Sum/max across ALL series: /metrics
# can briefly expose several registries mid-restart.
_HEARTBEAT_SERIES = (
    ("iteration_tokens_total_count", sum),
    ("num_requests_running", max),
    ("num_requests_waiting", max),
)


def _int_env(name: str, default: int) -> int:
    """os.environ[name] as int; unset/empty -> default, garbage -> default + warning."""
    raw = os.getenv(name)
    if raw is None or not raw.strip():
        return default
    try:
        return int(raw)
    except ValueError:
        logger.warning("%s=%r is not an integer -- using %d", name, raw, default)
        return default


def _scrape_heartbeat(host: str, port: int):
    """(iters, running, waiting) floats from /metrics; None per unreadable value.
    Raises requests.ConnectionError when the instance is unreachable (dead)."""
    try:
        txt = requests.get(f"http://{host}:{port}/metrics", timeout=3).text
    except requests.exceptions.ConnectTimeout:
        # Saturation, not death: the caller's grace window decides.
        return None, None, None
    except requests.ConnectionError:
        raise
    except Exception:
        return None, None, None
    values = []
    for name, reduce_fn in _HEARTBEAT_SERIES:
        try:
            series = re.findall(rf"(?m)^vllm:{name}\S*\s+([0-9eE.+-]+)", txt)
            values.append(reduce_fn(float(x) for x in series) if series else None)
        except Exception:
            values.append(None)
    return tuple(values)


def _heartbeat_verdict(port: int, st: dict, iters, running, waiting, now: float, grace: int) -> bool:
    """Advance one port's heartbeat state (mutates st); True = instance alive.
    Branch order is semantic -- reorder only with the unit tests in hand."""
    read_ok = iters is not None or running is not None or waiting is not None
    has_work = (running is not None and running > 0) or (waiting is not None and waiting > 0)
    # Fresh baseline on either: legitimate idle (readable scrape, no work) or a
    # stepping engine -- ANY counter change counts, including the reset after an
    # engine restart (a ">" test would false-HUNG on the stale high baseline).
    # A blind scrape (all None) is neither: refreshing on it would silently
    # disable hang detection.
    if (read_ok and not has_work) or (
        iters is not None and (st["iter"] is None or iters != st["iter"])
    ):
        st["ts"] = now
        st["iter"] = iters
        st["hung"] = 0
        return True
    if iters is None:
        # Counter unreadable: alive only within grace.
        return now - st["ts"] <= grace
    if now - st["ts"] > grace:
        # Frozen with work past grace: require consecutive verdicts so one
        # confused scrape cannot start the restart escalation.
        st["hung"] += 1
        if st["hung"] >= HANG_CONSECUTIVE:
            logger.error(
                "vLLM instance on port %s: scheduler heartbeat frozen >%ss "
                "over %d consecutive checks with running=%s waiting=%s -- "
                "reporting unhealthy for restart",
                port, grace, st["hung"], running, waiting,
            )
            return False
        return True
    st["hung"] = 0
    return True


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
        self._hb = {}  # per-port heartbeat state; outlives engine restarts

    def get_config_summary(self) -> dict:
        """Public config snapshot for observability (metrics exporter).

        Zero for the numeric fields means "not passed explicitly" — the
        effective vLLM default is not known to mlnode.
        """
        return {
            "model": self.model,
            "dtype": self.dtype,
            "max_num_seqs": self._get_arg_value("--max-num-seqs", default=0),
            "max_model_len": self._get_arg_value("--max-model-len", default=0),
            "tensor_parallel_size": self._get_arg_value("--tensor-parallel-size", default=0),
            "pipeline_parallel_size": self._get_arg_value("--pipeline-parallel-size", default=0),
        }

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
            # Metrics trust contract: no vendor telemetry from vLLM
            env["VLLM_NO_USAGE_STATS"] = "1"

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
        # Scheduler-heartbeat liveness. vLLM /health returns 200 even when the
        # scheduler is deadlocked (check_health only reads the `errored` flag)
        # and misses its deadline while merely busy. The iteration counter
        # advances on every engine step and freezes only on a real stall; the
        # grace window also absorbs the counter's observed pre-stall flatline.
        grace = _int_env("MLNODE_HANG_GRACE_SEC", 120)
        now = time.time()
        healthy = True
        # Check every instance: a hang must not hide behind a healthy sibling.
        for port in range(self.VLLM_PORT + 1, self.VLLM_PORT + len(self.processes) + 1):
            try:
                iters, running, waiting = _scrape_heartbeat(self.VLLM_HOST, port)
            except requests.ConnectionError:
                # Refused/unreachable -> this instance is dead.
                return False
            st = self._hb.setdefault(port, {"ts": now, "iter": iters, "hung": 0})
            if grace <= 0:
                # Detection disabled: only process death / refusal mark unhealthy.
                continue
            ok = _heartbeat_verdict(port, st, iters, running, waiting, now, grace)
            healthy = healthy and ok
        return healthy

    def get_error_if_exist(self) -> Optional[str]:
        for p in self.processes:
            if p.stderr:
                err = p.stderr.read().strip()
                if err:
                    return err
        return None
