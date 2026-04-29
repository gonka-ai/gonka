"""Send PoC inference requests to the inference server, then re-run each as a
validation request on the validation server.

Validation mode: the validation server receives the inference sphere_k_steps and
uses them to seed every decode step so both servers run identical forward passes.
Any k-id difference at a step counts as one hardware mismatch (no cascading).

Usage:
    python poc_compare.py [--inference URL] [--validation URL] [--n N]
                          [--block-hash HASH] [--public-key KEY]
                          [--nonce NONCE] [--seq-len SEQ_LEN]
                          [--max-tokens MAX_TOKENS] [--output FILE]
"""
import argparse
import json
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Dict, Any, Optional
import hashlib
import os

import requests


def get_server_model(base_url: str, timeout: int = 10) -> str:
    """Fetch the first model name from /v1/models."""
    resp = requests.get(f"{base_url}/v1/models", timeout=timeout)
    resp.raise_for_status()
    data = resp.json()
    return data["data"][0]["id"]


def send_inference_request(
    base_url: str,
    block_hash: str,
    public_key: str,
    nonces: List[int],
    seq_len: int,
    max_tokens: int,
    model: str,
    timeout: int = 600,
) -> Dict[str, Any]:
    url = f"{base_url}/api/v1/pow/generate"
    payload = {
        "block_hash": block_hash,
        "block_height": 100,
        "public_key": public_key,
        "node_id": 0,
        "node_count": 1,
        "nonces": nonces,
        "params": {
            "model": model,
            "seq_len": seq_len,
            "k_dim": 12,
            "max_tokens": max_tokens,
        },
        "wait": True,
    }
    resp = requests.post(url, json=payload, timeout=timeout)
    resp.raise_for_status()
    return resp.json()


def send_validation_request(
    base_url: str,
    block_hash: str,
    public_key: str,
    nonces: List[int],
    seq_len: int,
    max_tokens: int,
    inference_sphere_k_steps: Dict[int, List[int]],
    model: str,
    timeout: int = 600,
) -> Dict[str, Any]:
    """Same as inference but includes inference_k_steps so the server tracks
    deviations without cascading."""
    url = f"{base_url}/api/v1/pow/generate"
    payload = {
        "block_hash": block_hash,
        "block_height": 100,
        "public_key": public_key,
        "node_id": 0,
        "node_count": 1,
        "nonces": nonces,
        "params": {
            "model": model,
            "seq_len": seq_len,
            "k_dim": 12,
            "max_tokens": max_tokens,
        },
        "wait": True,
        "inference_k_points_steps": inference_sphere_k_steps,
    }
    resp = requests.post(url, json=payload, timeout=timeout)
    resp.raise_for_status()
    return resp.json()


def extract_artifact(response: Dict[str, Any], type: str="inference") -> Dict[str, Any]:
    all_nonces_artifacts = {}
    for art in response.get("artifacts", []):
        nonce = art.get("nonce")
        sphere_k_steps = art.get("k_points_steps")
        if nonce is not None and sphere_k_steps is not None:
            all_nonces_artifacts[nonce] = {}
            if type == "inference":
                all_nonces_artifacts[nonce] = sphere_k_steps
            elif type == "validation":
                n_sphere_mismatches = art.get("n_sphere_mismatches")
                all_nonces_artifacts[nonce]["sphere_k_steps"] = sphere_k_steps
                all_nonces_artifacts[nonce]["n_sphere_mismatches"] = n_sphere_mismatches
            else:
                raise ValueError(f"Unknown artifact type: {type}")
        else:
            raise ValueError(f"Nonce not found in response artifacts")

    return all_nonces_artifacts


def wait_for_server(url: str, timeout: int = 300, poll: float = 5.0):
    health_url = f"{url}/health"
    deadline = time.time() + timeout
    print(f"  Waiting for {url} ...", end="", flush=True)
    while time.time() < deadline:
        try:
            r = requests.get(health_url, timeout=5)
            if r.status_code == 200:
                print(" ready.")
                return
        except requests.RequestException:
            pass
        time.sleep(poll)
        print(".", end="", flush=True)
    print()
    raise TimeoutError(f"Server {url} did not become ready within {timeout}s")


def random_block_hash() -> str:
    return hashlib.sha256(os.urandom(32)).hexdigest()


def main():
    parser = argparse.ArgumentParser(
        description="Compare PoC sphere_k_steps via inference+validation requests.")
    parser.add_argument("--inference", default="http://localhost:8000")
    parser.add_argument("--validation", default="http://localhost:8001")
    parser.add_argument("--n", type=int, default=1, help="Number of paired runs")
    parser.add_argument("--num-hashes", default=None, type=int)
    parser.add_argument("--block-hash", default="deadbeef" * 8)
    parser.add_argument("--public-key", default="cafebabe" * 8)
    parser.add_argument("--num-nonces", default=None, type=int)
    parser.add_argument("--nonce", type=int, default=42)
    parser.add_argument("--seq-len", type=int, default=256)
    parser.add_argument("--max-tokens", type=int, default=8)
    parser.add_argument("--output", default=None)
    parser.add_argument("--results-dir", default="results")
    parser.add_argument("--no-wait", action="store_true")
    args = parser.parse_args()

    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    run_dir = Path(args.results_dir) / ts
    run_dir.mkdir(parents=True, exist_ok=True)
    output_path = Path(args.output) if args.output else run_dir / "artifacts.json"

    if not args.no_wait:
        print("Waiting for servers to be ready...")
        wait_for_server(args.inference)
        wait_for_server(args.validation)

    print("Fetching model names from servers...")
    inf_model = get_server_model(args.inference)
    val_model = get_server_model(args.validation)
    print(f"  inference  model: {inf_model}")
    print(f"  validation model: {val_model}")

    print(f"  inference  -> {args.inference}")
    print(f"  validation -> {args.validation}\n")

    runs: List[Dict[str, Any]] = []
    total_mismatches = 0

    if args.num_nonces is not None:
        nonces = list(range(args.num_nonces))
    else:
        nonces = [args.nonce]
    
    if args.num_hashes is not None:
        hashes = [random_block_hash() for _ in range(args.num_hashes)]
    else:
        hashes = [args.block_hash]

    for block_hash in hashes:
        for i in range(args.n):
            print(f"[{i+1}/{args.n}] Inference ...", end=" ", flush=True)
            t0 = time.perf_counter()
            resp_inf = send_inference_request(
                args.inference,
                block_hash,
                args.public_key,
                nonces,
                args.seq_len,
                args.max_tokens,
                inf_model,
            )
            t_inf = time.perf_counter() - t0
            inf_steps = extract_artifact(resp_inf)
            print(f"done ({t_inf:.2f}s)")

            print(f"[{i+1}/{args.n}] Validation ...", end=" ", flush=True)
            t0 = time.perf_counter()
            resp_val = send_validation_request(
                args.validation,
                block_hash,
                args.public_key,
                nonces,
                args.seq_len,
                args.max_tokens,
                inf_steps,
                val_model,
            )
            t_val = time.perf_counter() - t0
            val_art = extract_artifact(resp_val, type="validation")
            print(f"done ({t_val:.2f}s)")

            for nonce in nonces:
                n_mismatches_client = val_art.get(nonce, {}).get("n_sphere_mismatches", -1)
                total_mismatches += n_mismatches_client
                runs.append({
                    "run": i,
                    "nonce": nonce,
                    "block_hash": block_hash,
                    "inference_sphere_k_steps": inf_steps[nonce],
                    "validation_sphere_k_steps": val_art.get(nonce, {}).get("sphere_k_steps", []),
                    "inference_elapsed_s": t_inf,
                    "n_sphere_mismatches": n_mismatches_client,
                    "validation_elapsed_s": t_val,
                })

    print("=" * 60)
    print("SUMMARY")
    print(f"Total mismatches: {total_mismatches}")
    print(f"Total runs: {args.n*len(hashes)*len(nonces)}")

    all_inf_k = [k for r in runs for k in r["inference_sphere_k_steps"]]
    all_val_k = [k for r in runs for k in r["validation_sphere_k_steps"]]
    from collections import Counter
    print(f"\n  Inference k-distribution : {dict(sorted(Counter(all_inf_k).items()))}")
    print(f"  Validation k-distribution: {dict(sorted(Counter(all_val_k).items()))}")

    artifact = {
        "meta": {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "run_dir": str(run_dir),
            "inference_url": args.inference,
            "validation_url": args.validation,
            "max_tokens": args.max_tokens,
            "inference_model": inf_model,
            "validation_model": val_model,
        },
        "runs": runs,
    }

    with open(output_path, "w") as f:
        json.dump(artifact, f, indent=2)
    print(f"\nArtifacts saved to {output_path}")


if __name__ == "__main__":
    main()
