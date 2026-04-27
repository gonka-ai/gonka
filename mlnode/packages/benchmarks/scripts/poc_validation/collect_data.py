#!/usr/bin/env python3
"""
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
"""

from __future__ import annotations

import argparse
import base64
import itertools
import json
import os
import signal
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional, Set, Tuple

import numpy as np
import requests


DEFAULT_EXP_NAME = "poc_validation"

BENCHMARKS_DIR = Path(__file__).resolve().parents[2]
DATA_ROOT = BENCHMARKS_DIR / "data" / "poc_calidation"
DEFAULT_CONFIG_PATH = Path(__file__).resolve().with_name("config.json")

# Global shutdown event for Ctrl-C handling
shutdown_event = threading.Event()
_sigint_count = 0
_sigint_lock = threading.Lock()


def signal_handler(signum, frame) -> None:
    """Handle Ctrl-C by initiating shutdown; force-exit on repeated Ctrl-C."""
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
    }


def decode_vector(b64: str) -> np.ndarray:
    """Decode base64 FP16 little-endian to FP32."""
    data = base64.b64decode(b64)
    f16 = np.frombuffer(data, dtype="<f2")
    return f16.astype(np.float32)


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
    if not DATA_ROOT.exists():
        return None
    matching = sorted(
        [d for d in DATA_ROOT.iterdir() if d.is_dir() and d.name.startswith(f"{exp_name}_")],
        key=lambda d: d.name,
        reverse=True,
    )
    return matching[0] if matching else None


def get_completed_tasks(out_dir: Path) -> Set[str]:
    """Get completed task keys that already have successful data."""
    completed: Set[str] = set()
    for json_file in out_dir.glob("*.json"):
        if json_file.name == "run_config.json":
            continue
        try:
            data = json.loads(json_file.read_text(encoding="utf-8"))
            if "error" not in data and data.get("artifacts"):
                completed.add(json_file.stem)
        except Exception:
            pass
    return completed


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


def main() -> None:
    signal.signal(signal.SIGINT, signal_handler)

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
    args = parser.parse_args()

    config_path = Path(args.config).resolve()
    if not config_path.exists():
        print(f"Error: config file not found: {config_path}")
        sys.exit(1)

    config = json.loads(config_path.read_text(encoding="utf-8"))
    if "model" not in config:
        print("Error: config must include 'model' field")
        sys.exit(1)
    if "servers" not in config or not isinstance(config["servers"], dict) or not config["servers"]:
        print("Error: config must include non-empty 'servers' map")
        sys.exit(1)

    if "block_hashes" in config:
        block_hashes = config["block_hashes"]
    else:
        block_hashes = [config["block_hash"]]

    if "public_keys" in config:
        public_keys = config["public_keys"]
    else:
        public_keys = [config["public_key"]]

    seeds: List[Tuple[str, str]] = list(itertools.product(block_hashes, public_keys))
    multi_seed = len(seeds) > 1

    DATA_ROOT.mkdir(parents=True, exist_ok=True)
    exp_name = args.exp_name

    if args.continue_run:
        out_dir = find_latest_run(exp_name)
        if out_dir is None:
            print(f"No previous run found for '{exp_name}', starting fresh")
            args.continue_run = False

    if not args.continue_run:
        timestamp = datetime.now().strftime("%Y-%m-%d_%H%M%S")
        out_dir = DATA_ROOT / f"{exp_name}_{timestamp}"
        out_dir.mkdir(parents=True, exist_ok=True)

    completed = get_completed_tasks(out_dir) if args.continue_run else set()

    server_entries = list(config["servers"].items())
    vllm_runtime_probe = {name: probe_vllm(url) for name, url in server_entries}
    run_config = {
        "exp_name": exp_name,
        "timestamp": datetime.now().isoformat(),
        "artifact_dir": str(out_dir),
        "data_root": str(DATA_ROOT),
        "input_config_path": str(config_path),
        "config": config,
        "vllm_runtime_probe": vllm_runtime_probe,
        "cli": {
            "continue_run": bool(args.continue_run),
            "config": str(config_path),
        },
    }
    (out_dir / "run_config.json").write_text(
        json.dumps(run_config, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )

    url_to_tasks: Dict[str, List[Tuple[str, str, str]]] = {}
    for name, url in server_entries:
        for block_hash, public_key in seeds:
            task_key = get_task_key(name, block_hash, public_key, multi_seed)
            if task_key not in completed:
                url_to_tasks.setdefault(url, []).append((name, block_hash, public_key))

    total_tasks = sum(len(tasks) for tasks in url_to_tasks.values())

    print(f"EXP_NAME: {exp_name}")
    print(f"Output: {out_dir}")
    print(f"Model: {config['model']}")
    print(f"Servers: {list(config['servers'].keys())}")
    print(f"Seeds: {len(seeds)} combinations")
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

    print(f"\nDone. Results in {out_dir}")


if __name__ == "__main__":
    main()

