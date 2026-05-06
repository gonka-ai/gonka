#!/usr/bin/env python3
"""
<<<<<<< HEAD
PoC artifact data collection script for mlnode benchmarks.

This is adapted from vllm/scripts/collect_data.py with mlnode-specific output:
- Saves artifacts under benchmarks/data/poc_calidation/{EXP_NAME}_{timestamp}
- Stores a run config JSON that includes a vLLM runtime probe (models/health/version)
- Stores the input config only inside run_config.json (no separate config.json file)
- Adds timing and nonces_per_min metrics to each saved artifact file

Usage:
    python packages/benchmarks/poc_validation/collect_data.py
    python packages/benchmarks/poc_validation/collect_data.py --config packages/benchmarks/poc_validation/config.json
    python packages/benchmarks/poc_validation/collect_data.py --continue
=======
Streaming PoC collection aligned with `test.py`.

Starts continuous PoC generation through the mlnode API (port 8080) using
`/api/v1/inference/pow/init/generate`. That endpoint fans out work to all
healthy vLLM backends, so the collected nonces reflect the combined throughput
of all instances behind the server.

When the remote server cannot reach this machine directly (e.g. firewalled
inbound), the script can set up an SSH reverse tunnel automatically. Add an
"ssh" section to the config JSON:

    "ssh": {
        "host": "<SSH_HOST>",
        "port": 22,
        "user": "root",
        "key": "/path/to/ssh/key"
    }

The tunnel maps the receiver port on the remote host back to localhost, so
vLLM callbacks go through SSH instead of requiring open inbound ports.
>>>>>>> origin/testnet/latest-in-v0.2.12
"""

from __future__ import annotations

import argparse
<<<<<<< HEAD
=======
import atexit
>>>>>>> origin/testnet/latest-in-v0.2.12
import base64
import itertools
import json
import os
import signal
<<<<<<< HEAD
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
=======
import subprocess
import sys
import threading
import time
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
>>>>>>> origin/testnet/latest-in-v0.2.12
from pathlib import Path
from typing import Any, Dict, List, Optional, Set, Tuple

import numpy as np
import requests


<<<<<<< HEAD
DEFAULT_EXP_NAME = "poc_validation"

BENCHMARKS_DIR = Path(__file__).resolve().parents[2]
DATA_ROOT = BENCHMARKS_DIR / "data" / "poc_calidation"
DEFAULT_CONFIG_PATH = Path(__file__).resolve().with_name("config.json")

# Global shutdown event for Ctrl-C handling
=======
DEFAULT_EXP_NAME = "poc_validation_stream"
DEFAULT_WARMUP_SECONDS = 5
DEFAULT_MEASUREMENT_SECONDS = 30
DEFAULT_PROGRESS_INTERVAL_SECONDS = 10
DEFAULT_RECEIVER_PORT = 9999
DEFAULT_SSH_KEY = "/root/workspace/.ssh/vast_b200"

BENCHMARKS_DIR = Path(__file__).resolve().parents[2]
DATA_ROOT = BENCHMARKS_DIR / "data" / "experiments"
DEFAULT_CONFIG_PATH = Path(__file__).resolve().with_name("config.json")

>>>>>>> origin/testnet/latest-in-v0.2.12
shutdown_event = threading.Event()
_sigint_count = 0
_sigint_lock = threading.Lock()


def signal_handler(signum, frame) -> None:
<<<<<<< HEAD
    """Handle Ctrl-C by initiating shutdown; force-exit on repeated Ctrl-C."""
=======
>>>>>>> origin/testnet/latest-in-v0.2.12
    del signum, frame
    global _sigint_count
    with _sigint_lock:
        _sigint_count += 1
        count = _sigint_count

    shutdown_event.set()
    if count == 1:
        print("\n\nInterrupt received, shutting down... (press Ctrl-C again to force exit)")
    else:
        print("\n\nSecond interrupt received, forcing exit now.")
        os._exit(130)


<<<<<<< HEAD
def api_call(url: str, endpoint: str, method: str = "POST", json_data: Optional[dict] = None) -> dict:
    """Make API call to server."""
    if shutdown_event.is_set():
        raise RuntimeError("Cancelled")
    full_url = f"{url.rstrip('/')}{endpoint}"
    if method == "GET":
        resp = requests.get(full_url, timeout=30)
    else:
        resp = requests.post(full_url, json=json_data, timeout=600)
    resp.raise_for_status()
    return resp.json()


def safe_http_get(url: str, timeout: int = 5) -> Dict[str, Any]:
    """GET helper that never raises; used for runtime probing."""
    try:
        resp = requests.get(url, timeout=timeout)
        body: Any
        try:
            body = resp.json()
=======
def safe_http_get(url: str, timeout: int = 5) -> Dict[str, Any]:
    try:
        resp = requests.get(url, timeout=timeout)
        try:
            body: Any = resp.json()
>>>>>>> origin/testnet/latest-in-v0.2.12
        except Exception:
            body = resp.text[:5000]
        return {
            "ok": True,
            "status_code": resp.status_code,
            "body": body,
        }
    except Exception as exc:
        return {
            "ok": False,
            "error": repr(exc),
        }


<<<<<<< HEAD
def probe_vllm(base_url: str) -> Dict[str, Any]:
    """Collect runtime vLLM info similar to inference_validation/inference.py."""
    base = base_url.rstrip("/")
    models_info = safe_http_get(f"{base}/v1/models")
    served_model_ids: List[str] = []
    if models_info.get("ok") and isinstance(models_info.get("body"), dict):
        data = models_info["body"].get("data", [])
        if isinstance(data, list):
            for item in data:
                if isinstance(item, dict) and item.get("id"):
                    served_model_ids.append(str(item["id"]))

    return {
        "base_url": base,
        "timestamp": datetime.now().isoformat(),
        "served_model_ids": served_model_ids,
        "models": models_info,
        "health": safe_http_get(f"{base}/health"),
        "version": safe_http_get(f"{base}/version"),
=======
def probe_server(base_url: str) -> Dict[str, Any]:
    base = base_url.rstrip("/")
    return {
        "base_url": base,
        "timestamp": datetime.now().isoformat(),
        "models": safe_http_get(f"{base}/v1/models"),
        "health": safe_http_get(f"{base}/health"),
        "version": safe_http_get(f"{base}/version"),
        "pow_status": safe_http_get(f"{base}/api/v1/inference/pow/status"),
        "inference_status": safe_http_get(f"{base}/api/v1/inference/up/status"),
>>>>>>> origin/testnet/latest-in-v0.2.12
    }


def decode_vector(b64: str) -> np.ndarray:
<<<<<<< HEAD
    """Decode base64 FP16 little-endian to FP32."""
=======
>>>>>>> origin/testnet/latest-in-v0.2.12
    data = base64.b64decode(b64)
    f16 = np.frombuffer(data, dtype="<f2")
    return f16.astype(np.float32)


<<<<<<< HEAD
def collect_from_server(name: str, url: str, config: dict, block_hash: str, public_key: str) -> dict:
    """Collect data from a single server for a specific seed."""
    # Stop any running generation
    try:
        api_call(url, "/api/v1/pow/stop")
    except Exception:
        pass  # Ignore if nothing is currently running

    requested_nonces = list(range(int(config.get("nonce_count", 500))))

    gen_config = {
        "block_hash": block_hash,
        "block_height": int(config.get("block_height", 100)),
        "public_key": public_key,
        "node_id": 0,
        "node_count": 1,
        "nonces": requested_nonces,
        "params": {
            "model": config["model"],
            "seq_len": int(config.get("seq_len", 256)),
            "k_dim": int(config.get("k_dim", 12)),
        },
        "batch_size": int(config.get("batch_size", 128)),
        "wait": True,
    }

    started_at = time.time()
    started_at_iso = datetime.now().isoformat()
    result = api_call(url, "/api/v1/pow/generate", json_data=gen_config)
    finished_at = time.time()
    finished_at_iso = datetime.now().isoformat()

    artifacts = result.get("artifacts", [])
    encoding = result.get(
        "encoding",
        {"dtype": "f16", "k_dim": int(config.get("k_dim", 12)), "endian": "le"},
    )

    decoded_vectors = []
    for artifact in artifacts:
        try:
            vec = decode_vector(artifact["vector_b64"])
            decoded_vectors.append(vec.tolist())
        except Exception:
            decoded_vectors.append(None)

    elapsed_seconds = max(0.0, finished_at - started_at)
    collected_nonce_count = len(artifacts)
    nonces_per_min = (collected_nonce_count / elapsed_seconds * 60.0) if elapsed_seconds > 0 else 0.0

    return {
        "server_name": name,
        "server_url": url,
        "block_hash": block_hash,
        "public_key": public_key,
        "requested_nonce_count": len(requested_nonces),
        "collected_nonce_count": collected_nonce_count,
        "nonces": [a["nonce"] for a in artifacts],
        "artifacts": artifacts,
        "vectors": decoded_vectors,
        "encoding": encoding,
        "timing": {
            "started_at": started_at_iso,
            "finished_at": finished_at_iso,
            "elapsed_seconds": elapsed_seconds,
            "nonces_per_min": nonces_per_min,
        },
    }


def find_latest_run(exp_name: str) -> Optional[Path]:
    """Find the most recent output directory for a given experiment name."""
=======
class BatchReceiver:
    def __init__(self, port: int):
        self.port = port
        self._proof_batches: List[dict] = []
        self._server: Optional[ThreadingHTTPServer] = None
        self._thread: Optional[threading.Thread] = None
        self._lock = threading.Lock()

    def _make_handler(self):
        receiver = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                del format, args

            def _send_json(self, data: dict, status: int = 200):
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(data).encode("utf-8"))

            def _count_nonces(self, batch: dict) -> int:
                artifacts = batch.get("artifacts", [])
                if isinstance(artifacts, list):
                    return len(artifacts)
                nonces = batch.get("nonces", [])
                if isinstance(nonces, list):
                    return len(nonces)
                return 0

            def do_GET(self):
                if self.path == "/health":
                    self._send_json({"status": "OK"})
                    return
                if self.path == "/stats":
                    with receiver._lock:
                        batch_sizes = [self._count_nonces(batch) for batch in receiver._proof_batches]
                    total_nonces = sum(batch_sizes)
                    avg_batch_size = sum(batch_sizes) / len(batch_sizes) if batch_sizes else 0
                    self._send_json(
                        {
                            "total_nonces": total_nonces,
                            "batch_count": len(batch_sizes),
                            "batch_sizes": batch_sizes,
                            "avg_batch_size": avg_batch_size,
                        }
                    )
                    return
                if self.path == "/batches":
                    with receiver._lock:
                        batches = list(receiver._proof_batches)
                    self._send_json({"batches": batches})
                    return
                self._send_json({"error": "Not found"}, 404)

            def do_POST(self):
                content_length = int(self.headers.get("Content-Length", 0))
                try:
                    body = self.rfile.read(content_length).decode("utf-8") if content_length > 0 else "{}"
                except ConnectionResetError:
                    return
                try:
                    data = json.loads(body)
                except json.JSONDecodeError:
                    self._send_json({"error": "Invalid JSON"}, 400)
                    return

                if self.path == "/generated":
                    with receiver._lock:
                        receiver._proof_batches.append(data)
                    self._send_json({"message": "OK"})
                    return
                if self.path == "/clear":
                    with receiver._lock:
                        receiver._proof_batches.clear()
                    self._send_json({"message": "Cleared"})
                    return
                self._send_json({"error": "Not found"}, 404)

        return Handler

    def start(self) -> None:
        if self._server is not None:
            return
        self._server = ThreadingHTTPServer(("0.0.0.0", self.port), self._make_handler())
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def wait_until_ready(self, timeout_s: int = 30) -> bool:
        deadline = time.time() + timeout_s
        while time.time() < deadline:
            try:
                response = requests.get(f"http://127.0.0.1:{self.port}/health", timeout=2)
                if response.status_code == 200:
                    return True
            except requests.exceptions.RequestException:
                pass
            time.sleep(0.5)
        return False

    def stop(self) -> None:
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
            self._server = None
        if self._thread is not None:
            self._thread.join(timeout=5)
            self._thread = None

    def clear(self) -> None:
        with self._lock:
            self._proof_batches.clear()

    def stats(self) -> Dict[str, Any]:
        with self._lock:
            batch_sizes = [len(batch.get("artifacts", [])) for batch in self._proof_batches]
        total_nonces = sum(batch_sizes)
        avg_batch_size = sum(batch_sizes) / len(batch_sizes) if batch_sizes else 0
        return {
            "total_nonces": total_nonces,
            "batch_count": len(batch_sizes),
            "batch_sizes": batch_sizes,
            "avg_batch_size": avg_batch_size,
        }

    def collected_artifacts(self) -> List[dict]:
        with self._lock:
            batches = list(self._proof_batches)
        artifacts: List[dict] = []
        for batch in batches:
            batch_artifacts = batch.get("artifacts", [])
            if isinstance(batch_artifacts, list):
                artifacts.extend(batch_artifacts)
        return artifacts


class SSHTunnel:
    """Manages an SSH reverse tunnel as a subprocess."""

    def __init__(
        self,
        ssh_host: str,
        ssh_port: int,
        ssh_user: str,
        ssh_key: str,
        remote_port: int,
        local_port: int,
    ):
        self.ssh_host = ssh_host
        self.ssh_port = ssh_port
        self.ssh_user = ssh_user
        self.ssh_key = ssh_key
        self.remote_port = remote_port
        self.local_port = local_port
        self._proc: Optional[subprocess.Popen] = None

    def start(self, timeout: int = 15) -> None:
        cmd = [
            "ssh",
            "-i", self.ssh_key,
            "-R", f"{self.remote_port}:127.0.0.1:{self.local_port}",
            "-p", str(self.ssh_port),
            f"{self.ssh_user}@{self.ssh_host}",
            "-N",
            "-o", "StrictHostKeyChecking=no",
            "-o", f"ConnectTimeout={timeout}",
            "-o", "ServerAliveInterval=30",
            "-o", "ExitOnForwardFailure=yes",
        ]
        self._proc = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        time.sleep(3)
        if self._proc.poll() is not None:
            _, stderr = self._proc.communicate(timeout=5)
            raise RuntimeError(
                f"SSH tunnel failed (exit {self._proc.returncode}): {stderr.decode().strip()}"
            )
        print(f"  SSH tunnel: {self.ssh_host}:{self.remote_port} -> localhost:{self.local_port}")
        atexit.register(self.stop)

    def stop(self) -> None:
        if self._proc is not None and self._proc.poll() is None:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self._proc.kill()
            self._proc = None

    @property
    def alive(self) -> bool:
        return self._proc is not None and self._proc.poll() is None


def get_output_filename(server_name: str, block_hash: str, public_key: str, multi_seed: bool) -> str:
    if multi_seed:
        return f"poc_artifacts_{server_name}_{block_hash}_{public_key}.json"
    return "poc_artifacts.json"


def get_task_key(server_name: str, block_hash: str, public_key: str, multi_seed: bool) -> str:
    if multi_seed:
        return f"{server_name}_{block_hash}_{public_key}"
    return server_name


def find_latest_run(exp_name: str) -> Optional[Path]:
>>>>>>> origin/testnet/latest-in-v0.2.12
    if not DATA_ROOT.exists():
        return None
    matching = sorted(
        [d for d in DATA_ROOT.iterdir() if d.is_dir() and d.name.startswith(f"{exp_name}_")],
        key=lambda d: d.name,
        reverse=True,
    )
    return matching[0] if matching else None


def get_completed_tasks(out_dir: Path) -> Set[str]:
<<<<<<< HEAD
    """Get completed task keys that already have successful data."""
    completed: Set[str] = set()
    for json_file in out_dir.glob("*.json"):
        if json_file.name == "run_config.json":
            continue
        try:
            data = json.loads(json_file.read_text(encoding="utf-8"))
            if "error" not in data and data.get("artifacts"):
                completed.add(json_file.stem)
=======
    completed: Set[str] = set()
    for json_file in out_dir.glob("artifacts_*.json"):
        try:
            data = json.loads(json_file.read_text(encoding="utf-8"))
            if "error" in data or not data.get("artifacts"):
                continue
            stem = json_file.stem.replace("artifacts_", "", 1)
            completed.add(stem)
>>>>>>> origin/testnet/latest-in-v0.2.12
        except Exception:
            pass
    return completed


<<<<<<< HEAD
def get_output_filename(server_name: str, block_hash: str, public_key: str, multi_seed: bool) -> str:
    """Generate output filename based on seed mode."""
    if multi_seed:
        return f"artifacts_{server_name}_{block_hash}_{public_key}.json"
    return f"artifacts_{server_name}.json"


def get_task_key(server_name: str, block_hash: str, public_key: str, multi_seed: bool) -> str:
    """Generate task key for tracking completion."""
    if multi_seed:
        return f"{server_name}_{block_hash}_{public_key}"
    return server_name
=======
def stop_generation(server_url: str) -> None:
    try:
        response = requests.post(f"{server_url.rstrip('/')}/api/v1/inference/pow/stop", json={}, timeout=60)
        response.raise_for_status()
    except Exception:
        pass


def init_generate(server_url: str, payload: dict, max_retries: int = 12, retry_delay: int = 5) -> dict:
    url = f"{server_url.rstrip('/')}/api/v1/inference/pow/init/generate"
    for attempt in range(max_retries):
        if shutdown_event.is_set():
            raise RuntimeError("Cancelled")
        response = requests.post(url, json=payload, timeout=60)
        if response.status_code == 503 and attempt < max_retries - 1:
            print(f"  vLLM not ready (503), retrying in {retry_delay}s... ({attempt + 1}/{max_retries})")
            time.sleep(retry_delay)
            continue
        response.raise_for_status()
        return response.json()
    raise RuntimeError("Failed to initialize generation")


def collect_from_server(
    name: str,
    server_url: str,
    config: dict,
    block_hash: str,
    public_key: str,
    receiver: BatchReceiver,
    callback_url: str,
    warmup_seconds: int,
    measurement_seconds: int,
    progress_interval_seconds: int,
) -> dict:
    stop_generation(server_url)
    receiver.clear()

    payload = {
        "block_hash": block_hash,
        "block_height": int(config.get("block_height", 100)),
        "public_key": public_key,
        "node_id": int(config.get("node_id", 0)),
        "node_count": int(config.get("node_count", 1)),
        "batch_size": int(config.get("batch_size", 32)),
        "params": {
            "model": config["model"],
            "seq_len": int(config.get("seq_len", 256)),
            "k_dim": int(config.get("k_dim", 12)),
        },
        "url": callback_url,
    }

    init_response = init_generate(server_url, payload)

    if warmup_seconds > 0:
        print(f"  Warmup: {warmup_seconds}s")
        time.sleep(warmup_seconds)

    receiver.clear()
    measured_started_at = time.time()
    measured_started_at_iso = datetime.now().isoformat()

    elapsed = 0
    while elapsed < measurement_seconds and not shutdown_event.is_set():
        sleep_seconds = min(progress_interval_seconds, measurement_seconds - elapsed)
        time.sleep(sleep_seconds)
        elapsed += sleep_seconds
        stats = receiver.stats()
        print(f"  [{elapsed:>2d}s] {stats['total_nonces']} nonces")

    measured_finished_at = time.time()
    measured_finished_at_iso = datetime.now().isoformat()

    stop_generation(server_url)
    time.sleep(1.0)

    artifacts = receiver.collected_artifacts()
    decoded_vectors = []
    for artifact in artifacts:
        try:
            decoded_vectors.append(decode_vector(artifact["vector_b64"]).tolist())
        except Exception:
            decoded_vectors.append(None)

    stats = receiver.stats()
    elapsed_seconds = max(0.0, measured_finished_at - measured_started_at)
    nonces_per_min = (len(artifacts) / elapsed_seconds * 60.0) if elapsed_seconds > 0 else 0.0

    return {
        "collection_mode": "init_generate_callback",
        "server_name": name,
        "server_url": server_url,
        "block_hash": block_hash,
        "public_key": public_key,
        "init_response": init_response,
        "collected_nonce_count": len(artifacts),
        "nonces": [artifact["nonce"] for artifact in artifacts if "nonce" in artifact],
        "artifacts": artifacts,
        "vectors": decoded_vectors,
        "encoding": {
            "dtype": "f16",
            "k_dim": int(config.get("k_dim", 12)),
            "endian": "le",
        },
        "batching": stats,
        "timing": {
            "warmup_seconds": warmup_seconds,
            "measurement_seconds_requested": measurement_seconds,
            "started_at": measured_started_at_iso,
            "finished_at": measured_finished_at_iso,
            "elapsed_seconds": elapsed_seconds,
            "nonces_per_min": nonces_per_min,
        },
    }
>>>>>>> origin/testnet/latest-in-v0.2.12


def main() -> None:
    signal.signal(signal.SIGINT, signal_handler)

<<<<<<< HEAD
    parser = argparse.ArgumentParser(description="Collect PoC data from multiple servers")
    parser.add_argument(
        "--config",
        default=str(DEFAULT_CONFIG_PATH),
        help=f"Path to config JSON file (default: {DEFAULT_CONFIG_PATH})",
    )
    parser.add_argument(
        "--exp-name",
        default=DEFAULT_EXP_NAME,
        help=f"Experiment name prefix for the output directory (default: {DEFAULT_EXP_NAME})",
    )
    parser.add_argument(
        "--continue",
        dest="continue_run",
        action="store_true",
        help="Continue from latest run dir for this exp-name, skipping completed tasks",
    )
=======
    parser = argparse.ArgumentParser(description="Collect streamed PoC data across all vLLM instances")
    parser.add_argument("--config", default=str(DEFAULT_CONFIG_PATH), help="Path to config JSON file")
    parser.add_argument("--exp-name", default=DEFAULT_EXP_NAME, help="Experiment name prefix (used when --exp-dir is not set)")
    parser.add_argument("--exp-dir", type=Path, default=None, help="Write into an existing experiment directory instead of creating a new one.")
    parser.add_argument("--continue", dest="continue_run", action="store_true", help="Resume latest run")
    parser.add_argument("--warmup-seconds", type=int, default=DEFAULT_WARMUP_SECONDS)
    parser.add_argument("--measurement-seconds", type=int, default=DEFAULT_MEASUREMENT_SECONDS)
    parser.add_argument("--progress-interval-seconds", type=int, default=DEFAULT_PROGRESS_INTERVAL_SECONDS)
    parser.add_argument("--receiver-port", type=int, default=DEFAULT_RECEIVER_PORT)
    parser.add_argument(
        "--callback-host",
        default=os.environ.get("CALLBACK_HOST", "127.0.0.1"),
        help="Host/IP the remote server can reach for callback batches",
    )
    parser.add_argument("--ssh-key", default=None, help="Path to SSH private key (overrides config ssh.key)")
    parser.add_argument("--no-tunnel", action="store_true", help="Disable automatic SSH tunnel even if config has ssh section")
>>>>>>> origin/testnet/latest-in-v0.2.12
    args = parser.parse_args()

    config_path = Path(args.config).resolve()
    if not config_path.exists():
        print(f"Error: config file not found: {config_path}")
        sys.exit(1)

    config = json.loads(config_path.read_text(encoding="utf-8"))
    if "model" not in config:
<<<<<<< HEAD
        print("Error: config must include 'model' field")
=======
        print("Error: config must include 'model'")
>>>>>>> origin/testnet/latest-in-v0.2.12
        sys.exit(1)
    if "servers" not in config or not isinstance(config["servers"], dict) or not config["servers"]:
        print("Error: config must include non-empty 'servers' map")
        sys.exit(1)

<<<<<<< HEAD
    if "block_hashes" in config:
        block_hashes = config["block_hashes"]
    else:
        block_hashes = [config["block_hash"]]

    if "public_keys" in config:
        public_keys = config["public_keys"]
    else:
        public_keys = [config["public_key"]]

=======
    block_hashes = config.get("block_hashes", [config["block_hash"]])
    public_keys = config.get("public_keys", [config["public_key"]])
>>>>>>> origin/testnet/latest-in-v0.2.12
    seeds: List[Tuple[str, str]] = list(itertools.product(block_hashes, public_keys))
    multi_seed = len(seeds) > 1

    DATA_ROOT.mkdir(parents=True, exist_ok=True)
    exp_name = args.exp_name

<<<<<<< HEAD
    if args.continue_run:
        out_dir = find_latest_run(exp_name)
        if out_dir is None:
            print(f"No previous run found for '{exp_name}', starting fresh")
            args.continue_run = False

    if not args.continue_run:
=======
    if args.exp_dir:
        out_dir = args.exp_dir.resolve()
        out_dir.mkdir(parents=True, exist_ok=True)
    elif args.continue_run:
        out_dir = find_latest_run(exp_name)
        if out_dir is None:
            print(f"No previous run found for '{exp_name}', starting fresh")
            timestamp = datetime.now().strftime("%Y-%m-%d_%H%M%S")
            out_dir = DATA_ROOT / f"{exp_name}_{timestamp}"
            out_dir.mkdir(parents=True, exist_ok=True)
    else:
>>>>>>> origin/testnet/latest-in-v0.2.12
        timestamp = datetime.now().strftime("%Y-%m-%d_%H%M%S")
        out_dir = DATA_ROOT / f"{exp_name}_{timestamp}"
        out_dir.mkdir(parents=True, exist_ok=True)

    completed = get_completed_tasks(out_dir) if args.continue_run else set()

    server_entries = list(config["servers"].items())
<<<<<<< HEAD
    vllm_runtime_probe = {name: probe_vllm(url) for name, url in server_entries}
=======
    runtime_probe = {name: probe_server(url) for name, url in server_entries}
>>>>>>> origin/testnet/latest-in-v0.2.12
    run_config = {
        "exp_name": exp_name,
        "timestamp": datetime.now().isoformat(),
        "artifact_dir": str(out_dir),
        "data_root": str(DATA_ROOT),
        "input_config_path": str(config_path),
        "config": config,
<<<<<<< HEAD
        "vllm_runtime_probe": vllm_runtime_probe,
        "cli": {
            "continue_run": bool(args.continue_run),
            "config": str(config_path),
        },
    }
    (out_dir / "run_config.json").write_text(
=======
        "collection_mode": "init_generate_callback",
        "runtime_probe": runtime_probe,
        "cli": {
            "continue_run": bool(args.continue_run),
            "config": str(config_path),
            "warmup_seconds": args.warmup_seconds,
            "measurement_seconds": args.measurement_seconds,
            "progress_interval_seconds": args.progress_interval_seconds,
            "receiver_port": args.receiver_port,
            "callback_host": args.callback_host,
        },
    }
    (out_dir / "poc_config.json").write_text(
>>>>>>> origin/testnet/latest-in-v0.2.12
        json.dumps(run_config, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )

<<<<<<< HEAD
    url_to_tasks: Dict[str, List[Tuple[str, str, str]]] = {}
    for name, url in server_entries:
        for block_hash, public_key in seeds:
            task_key = get_task_key(name, block_hash, public_key, multi_seed)
            if task_key not in completed:
                url_to_tasks.setdefault(url, []).append((name, block_hash, public_key))

    total_tasks = sum(len(tasks) for tasks in url_to_tasks.values())

    print(f"EXP_NAME: {exp_name}")
=======
    receiver = BatchReceiver(args.receiver_port)
    receiver.start()
    if not receiver.wait_until_ready():
        print("Error: callback receiver failed to start")
        sys.exit(1)

    ssh_tunnel: Optional[SSHTunnel] = None
    callback_host = args.callback_host
    ssh_config = config.get("ssh")

    if ssh_config and not args.no_tunnel:
        ssh_key = args.ssh_key or ssh_config.get("key", DEFAULT_SSH_KEY)
        ssh_host = ssh_config["host"]
        ssh_port = int(ssh_config.get("port", 22))
        ssh_user = ssh_config.get("user", "root")

        print(f"Setting up SSH reverse tunnel to {ssh_host}:{ssh_port}...")
        ssh_tunnel = SSHTunnel(
            ssh_host=ssh_host,
            ssh_port=ssh_port,
            ssh_user=ssh_user,
            ssh_key=ssh_key,
            remote_port=args.receiver_port,
            local_port=args.receiver_port,
        )
        ssh_tunnel.start()
        callback_host = "127.0.0.1"

    callback_url = f"http://{callback_host}:{args.receiver_port}"
    print(f"Callback receiver: {callback_url}")
    if ssh_tunnel:
        print(f"  (via SSH tunnel to {ssh_tunnel.ssh_host})")
>>>>>>> origin/testnet/latest-in-v0.2.12
    print(f"Output: {out_dir}")
    print(f"Model: {config['model']}")
    print(f"Servers: {list(config['servers'].keys())}")
    print(f"Seeds: {len(seeds)} combinations")
<<<<<<< HEAD
    if multi_seed:
        print(f"  block_hashes: {block_hashes}")
        print(f"  public_keys: {public_keys}")
    print(f"Total tasks: {total_tasks}")
    print(f"Workers: {len(url_to_tasks)} (one per URL)")
    if completed:
        print(f"Skipping (already done): {len(completed)} tasks")
    print()

    def collect_all_seeds_for_url(url: str, task_list: List[Tuple[str, str, str]]):
        """Process all seeds for one URL sequentially. Returns list of results."""
        results = []
        for name, block_hash, public_key in task_list:
            if shutdown_event.is_set():
                break

            filename = get_output_filename(name, block_hash, public_key, multi_seed)
            try:
                result = collect_from_server(name, url, config, block_hash, public_key)
                (out_dir / filename).write_text(
                    json.dumps(result, indent=2, ensure_ascii=False) + "\n",
                    encoding="utf-8",
                )
                results.append(
                    (
                        name,
                        block_hash,
                        public_key,
                        int(result["collected_nonce_count"]),
                        float(result["timing"]["nonces_per_min"]),
                        None,
                    )
                )
            except Exception as exc:
                if shutdown_event.is_set():
                    break
                error_result = {
                    "server_name": name,
                    "server_url": url,
                    "block_hash": block_hash,
                    "public_key": public_key,
                    "error": str(exc),
                }
                (out_dir / filename).write_text(
                    json.dumps(error_result, indent=2, ensure_ascii=False) + "\n",
                    encoding="utf-8",
                )
                results.append((name, block_hash, public_key, 0, 0.0, str(exc)))
        return url, results

    if not url_to_tasks:
        print("No tasks to run.")
        return

    interrupted = False
    try:
        with ThreadPoolExecutor(max_workers=len(url_to_tasks)) as executor:
            futures = [
                executor.submit(collect_all_seeds_for_url, url, task_list)
                for url, task_list in url_to_tasks.items()
            ]

            pending = set(futures)
            while pending:
                if shutdown_event.is_set():
                    interrupted = True
                    for future in list(pending):
                        future.cancel()
                    break

                done_now = {future for future in pending if future.done()}
                if not done_now:
                    time.sleep(0.1)
                    continue

                for future in done_now:
                    pending.remove(future)
                    _, results = future.result()
                    for name, block_hash, public_key, nonce_count, nonces_per_min, error in results:
                        seed_str = f" [{block_hash}+{public_key}]" if multi_seed else ""
                        if error:
                            print(f"{name}{seed_str}: FAILED - {error}")
                        else:
                            print(
                                f"{name}{seed_str}: OK "
                                f"({nonce_count} artifacts, {nonces_per_min:.2f} nonces/min)"
                            )
    except KeyboardInterrupt:
        interrupted = True
        shutdown_event.set()
        print("\n\nInterrupt received, cancelling pending tasks...")

    if interrupted:
        print(f"\nInterrupted. Partial results in {out_dir}")
        print("Use --continue to resume from where you left off.")
        sys.exit(1)
=======
    print()

    try:
        for name, url in server_entries:
            for block_hash, public_key in seeds:
                task_key = get_task_key(name, block_hash, public_key, multi_seed)
                if task_key in completed:
                    print(f"{task_key}: skipped (already done)")
                    continue

                filename = get_output_filename(name, block_hash, public_key, multi_seed)
                seed_str = f" [{block_hash}+{public_key}]" if multi_seed else ""
                print(f"{name}{seed_str}: starting")
                try:
                    result = collect_from_server(
                        name=name,
                        server_url=url,
                        config=config,
                        block_hash=block_hash,
                        public_key=public_key,
                        receiver=receiver,
                        callback_url=callback_url,
                        warmup_seconds=args.warmup_seconds,
                        measurement_seconds=args.measurement_seconds,
                        progress_interval_seconds=args.progress_interval_seconds,
                    )
                    (out_dir / filename).write_text(
                        json.dumps(result, indent=2, ensure_ascii=False) + "\n",
                        encoding="utf-8",
                    )
                    print(
                        f"{name}{seed_str}: OK "
                        f"({result['collected_nonce_count']} artifacts, "
                        f"{result['timing']['nonces_per_min']:.2f} nonces/min)"
                    )
                except Exception as exc:
                    error_result = {
                        "server_name": name,
                        "server_url": url,
                        "block_hash": block_hash,
                        "public_key": public_key,
                        "error": str(exc),
                    }
                    (out_dir / filename).write_text(
                        json.dumps(error_result, indent=2, ensure_ascii=False) + "\n",
                        encoding="utf-8",
                    )
                    print(f"{name}{seed_str}: FAILED - {exc}")
    finally:
        receiver.stop()
        if ssh_tunnel is not None:
            ssh_tunnel.stop()
            print("SSH tunnel closed.")
>>>>>>> origin/testnet/latest-in-v0.2.12

    print(f"\nDone. Results in {out_dir}")


if __name__ == "__main__":
    main()
<<<<<<< HEAD

=======
>>>>>>> origin/testnet/latest-in-v0.2.12
