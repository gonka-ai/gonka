#!/usr/bin/env python3
"""
End-to-end MLNode PoC v2 validation against a deployed MLNode.

Reads an artifact JSON (model + pre-computed honest PoC vectors), then:
  1. Optionally deploys the model on the MLNode (`POST /api/v1/inference/up/async`)
     and waits until `is_running` is true.
  2. Starts continuous PoC generation across all healthy vLLM backends
     (`POST /api/v1/inference/pow/init/generate`).
  3. Polls `GET /api/v1/inference/pow/status` and reports full-system
     throughput as the sum of per-backend `nonces_per_second` (each backend
     is a vLLM replica processing a different group of nonces).
  4. Stops generation (`POST /api/v1/inference/pow/stop`).
  5. Sends the artifact's pre-computed (nonce, vector_b64) pairs to
     `POST /api/v1/inference/pow/generate` with `wait=true` and
     `validation.artifacts` set, so the server recomputes the same nonces and
     compares vectors against the artifact under the configured
     `dist_threshold`. Returns `{n_total, n_mismatch, p_value, fraud_detected}`.
  6. Writes a JSON + text report.

The script depends only on the Python standard library and `requests`; no
internal repo imports.

Artifact format:
{
  "model": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
  "seq_len": 1024,
  "k_dim": 12,
  "block_hash": "TEST_BLOCK",
  "block_height": 100,
  "public_key": "test_pub_keys",
  "node_id": 0,
  "node_count": 1,
  "dist_threshold": 0.2,
  "artifacts": [
    {"nonce": 0, "vector_b64": "..."},
    ...
  ],
  "source": "<provenance string>"
}

Required inputs (no defaults):
  --mlnode-url   base URL of the MLNode under test
  --model        target model id (e.g. Qwen/Qwen3-235B-A22B-Instruct-2507-FP8).
                 The script looks up artifacts/<sanitized model>.json. If
                 missing, it errors out and prints the make_artifact.py
                 invocation needed to bake one.

Optional:
  --artifact     explicit artifact path; overrides the model-based lookup.
  --threshold    L2 dist threshold (else artifact.dist_threshold else 0.2).
  --dtype, --tp-size, --max-model-len, --extra-arg
                 deploy params; layered on top of artifact.additional_args.

Usage:
  python validate.py --mlnode-url http://host:8080 \\
                     --model Qwen/Qwen3-235B-A22B-Instruct-2507-FP8
  python validate.py --mlnode-url http://host:8080 --model <id> \\
                     --tp-size 4 --max-model-len 240000 --threshold 0.2
  python validate.py --mlnode-url http://host:8080 --model <id> --skip-deploy
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

import requests


DEFAULT_THRESHOLD = 0.2
DEFAULT_WARMUP_SECONDS = 30
DEFAULT_MEASURE_SECONDS = 60
DEFAULT_BATCH_SIZE = 32
DEFAULT_DEPLOY_TIMEOUT_SECONDS = 1800  # 30 min for very large models
DEFAULT_VALIDATION_TIMEOUT_SECONDS = 900
DEFAULT_DOWNLOAD_TIMEOUT_SECONDS = 7200  # 2 h ceiling for very large weights

# Mirror of mlnode/packages/api/.../pow_v2_routes.py StatTestModel defaults.
# Held client-side so the script can report the full effective stat_test
# triple (the server doesn't echo defaults back in its response). Bump these
# if pow_v2_routes.StatTestModel changes its field defaults.
SERVER_DEFAULT_STAT_TEST: Dict[str, float] = {
    "dist_threshold": 0.02,
    "p_mismatch": 0.001,
    "fraud_threshold": 0.01,
}

API = "/api/v1"

# Golden reference artifacts (committed to the repo). Each file is the
# canonical, pre-computed PoC vector set for one model. validate.py reads
# from here; nothing in this directory is written at runtime.
REFERENCES_DIR = Path(__file__).resolve().parent / "artifacts"

# Per-run output root, matching the layout documented in
# mlnode/packages/benchmarks/scripts/README.md
# ("All scripts write into a unified directory under data/experiments/").
BENCHMARKS_DIR = Path(__file__).resolve().parents[2]
EXPERIMENTS_ROOT = BENCHMARKS_DIR / "data" / "experiments"


def model_to_safe_id(model: str) -> str:
    """Lower-cased model id with '/' and other separators collapsed to '-'.

    Used both as the reference filename inside `references/` and as the
    default experiment-name prefix. Never a path the caller controls.
    """
    safe = model.strip().lower()
    for ch in ("/", "\\", ":", " "):
        safe = safe.replace(ch, "-")
    while "--" in safe:
        safe = safe.replace("--", "-")
    return safe


def model_to_artifact_filename(model: str) -> str:
    return f"{model_to_safe_id(model)}.json"


def resolve_reference_path(model: str) -> Path:
    return REFERENCES_DIR / model_to_artifact_filename(model)


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def _get(url: str, timeout: float = 10.0) -> Tuple[int, Any]:
    r = requests.get(url, timeout=timeout)
    try:
        body = r.json()
    except ValueError:
        body = r.text
    return r.status_code, body


def _post(url: str, payload: Dict[str, Any], timeout: float = 60.0) -> Tuple[int, Any]:
    r = requests.post(url, json=payload, timeout=timeout)
    try:
        body = r.json()
    except ValueError:
        body = r.text
    return r.status_code, body


# ---------------------------------------------------------------------------
# MLNode operations
# ---------------------------------------------------------------------------


def get_model_status(base_url: str, hf_repo: str) -> Dict[str, Any]:
    """POST /api/v1/models/status -> {status: NOT_FOUND|PARTIAL|DOWNLOADING|DOWNLOADED, progress, error_message}."""
    code, body = _post(f"{base_url}{API}/models/status", {"hf_repo": hf_repo, "hf_commit": None})
    if code != 200 or not isinstance(body, dict):
        raise RuntimeError(f"POST /models/status failed: {code} {body}")
    return body


def start_model_download(base_url: str, hf_repo: str) -> Dict[str, Any]:
    """POST /api/v1/models/download. Idempotent: returns DOWNLOADED inline if already cached."""
    code, body = _post(f"{base_url}{API}/models/download", {"hf_repo": hf_repo, "hf_commit": None})
    # 202 = accepted (download started), 200 = already there in some builds
    if code not in (200, 202) or not isinstance(body, dict):
        raise RuntimeError(f"POST /models/download failed: {code} {body}")
    return body


def ensure_model_downloaded(
    base_url: str,
    hf_repo: str,
    download_timeout_s: float,
    poll_interval_s: float = 10.0,
) -> Dict[str, Any]:
    """Make sure the requested HF repo is fully cached on the MLNode.

    Returns a dict describing what happened:
      {action: "already_downloaded"|"downloaded"|"verified",
       elapsed_s: float, last_status: <body of /models/status>}

    The progress this prints is download elapsed time only -- the API does
    not expose byte-level progress. We rely on the server's own status
    transition (PARTIAL/NOT_FOUND/DOWNLOADING -> DOWNLOADED).
    """
    initial = get_model_status(base_url, hf_repo)
    initial_status = initial.get("status")

    if initial_status == "DOWNLOADED":
        return {"action": "already_downloaded", "elapsed_s": 0.0, "last_status": initial}

    print(f"  initial status: {initial_status} -- requesting download")
    start_resp = start_model_download(base_url, hf_repo)
    # If the server tells us right away the model is already there, treat as a verify.
    if start_resp.get("status") == "DOWNLOADED":
        return {"action": "verified", "elapsed_s": 0.0, "last_status": start_resp}

    start = time.time()
    last_print = 0.0
    while True:
        elapsed = time.time() - start
        body = get_model_status(base_url, hf_repo)
        st = body.get("status")
        if st == "DOWNLOADED":
            return {"action": "downloaded", "elapsed_s": elapsed, "last_status": body}
        if st == "NOT_FOUND" and elapsed > 30:
            # Status flipped back to NOT_FOUND while we were polling -- the download
            # must have failed without producing PARTIAL. Treat as hard error.
            raise RuntimeError(f"model download failed (status went to NOT_FOUND): {body}")
        if elapsed > download_timeout_s:
            raise RuntimeError(
                f"model download did not complete in {download_timeout_s}s; last status: {body}"
            )
        if elapsed - last_print >= poll_interval_s:
            prog = body.get("progress") or {}
            srv_elapsed = prog.get("elapsed_seconds")
            print(f"  [{int(elapsed):>4d}s] {st} (server elapsed: {srv_elapsed})")
            last_print = elapsed
        time.sleep(min(poll_interval_s, 5.0))


def get_inference_status(base_url: str) -> Dict[str, Any]:
    code, body = _get(f"{base_url}{API}/inference/up/status")
    if code != 200 or not isinstance(body, dict):
        raise RuntimeError(f"GET /inference/up/status failed: {code} {body}")
    return body


def deploy_model(
    base_url: str,
    model: str,
    dtype: str,
    additional_args: List[str],
    deploy_timeout_s: float,
) -> Dict[str, Any]:
    """Start vLLM if it's not already running and wait until it is."""
    status = get_inference_status(base_url)

    if status.get("is_running"):
        return {"action": "already_running", "elapsed_s": 0.0, "status": status}

    if status.get("is_starting"):
        print("  vLLM startup already in progress; waiting...")
    else:
        payload = {"model": model, "dtype": dtype, "additional_args": additional_args}
        code, body = _post(f"{base_url}{API}/inference/up/async", payload, timeout=60)
        if code == 409:
            # already running / already starting -- treat as benign
            print(f"  /inference/up/async returned 409: {body}")
        elif code != 200:
            raise RuntimeError(f"/inference/up/async failed: {code} {body}")

    start = time.time()
    last_print = 0.0
    while True:
        elapsed = time.time() - start
        status = get_inference_status(base_url)
        if status.get("is_running"):
            return {"action": "deployed", "elapsed_s": elapsed, "status": status}
        if status.get("status") in ("failed", "cancelled"):
            raise RuntimeError(f"vLLM startup failed: {status}")
        if elapsed > deploy_timeout_s:
            raise RuntimeError(
                f"vLLM did not become ready within {deploy_timeout_s}s; last status: {status}"
            )
        if elapsed - last_print >= 30:
            srv_elapsed = status.get("elapsed_seconds")
            print(f"  [{int(elapsed):>4d}s] still starting (server elapsed: {srv_elapsed})")
            last_print = elapsed
        time.sleep(5)


def get_pow_status(base_url: str) -> Dict[str, Any]:
    code, body = _get(f"{base_url}{API}/inference/pow/status")
    if code != 200 or not isinstance(body, dict):
        raise RuntimeError(f"GET /inference/pow/status failed: {code} {body}")
    return body


def init_generate(
    base_url: str,
    artifact: Dict[str, Any],
    batch_size: Optional[int],
    max_retries: int = 12,
    retry_delay: int = 5,
) -> Dict[str, Any]:
    """Start PoC generation across all healthy vLLM backends.

    No callback `url` is sent: throughput is measured from server-side counters
    via /pow/status, so we don't need to receive batches locally.

    `batch_size=None` means: do not include batch_size in the payload, so the
    server uses its POC_BATCH_SIZE_DEFAULT.
    """
    payload = {
        "block_hash": artifact["block_hash"],
        "block_height": int(artifact.get("block_height", 100)),
        "public_key": artifact["public_key"],
        "node_id": int(artifact.get("node_id", 0)),
        "node_count": int(artifact.get("node_count", 1)),
        "params": {
            "model": artifact["model"],
            "seq_len": int(artifact["seq_len"]),
            "k_dim": int(artifact["k_dim"]),
        },
    }
    if batch_size is not None:
        payload["batch_size"] = int(batch_size)
    url = f"{base_url}{API}/inference/pow/init/generate"
    last_err: Optional[str] = None
    for attempt in range(max_retries):
        r = requests.post(url, json=payload, timeout=60)
        if r.status_code == 200:
            return r.json()
        last_err = f"{r.status_code} {r.text}"
        if r.status_code == 503:
            print(f"  init/generate 503 (vLLM not ready); retry in {retry_delay}s ({attempt + 1}/{max_retries})")
            time.sleep(retry_delay)
            continue
        raise RuntimeError(f"/inference/pow/init/generate failed: {last_err}")
    raise RuntimeError(f"/inference/pow/init/generate gave up after {max_retries} retries: {last_err}")


def stop_generation(base_url: str) -> None:
    try:
        requests.post(f"{base_url}{API}/inference/pow/stop", json={}, timeout=60)
    except Exception:
        pass


def measure_throughput(
    base_url: str,
    warmup_s: int,
    measure_s: int,
    sample_interval_s: int = 10,
) -> Dict[str, Any]:
    """Sample /pow/status and report sum-of-backends nonces_per_second.

    Each backend is a vLLM replica processing a different group of nonces, so
    the system-level rate for the model is the sum across replicas.
    """
    if warmup_s > 0:
        print(f"  warmup: {warmup_s}s")
        time.sleep(warmup_s)

    samples: List[Dict[str, Any]] = []
    start = time.time()
    elapsed = 0
    while elapsed < measure_s:
        sleep_for = min(sample_interval_s, measure_s - elapsed)
        time.sleep(sleep_for)
        elapsed = int(time.time() - start)
        status = get_pow_status(base_url)
        backends = status.get("backends", []) or []
        per_backend_rate = []
        per_backend_processed = []
        for b in backends:
            stats = b.get("stats") or {}
            per_backend_rate.append(float(stats.get("nonces_per_second") or 0.0))
            per_backend_processed.append(int(stats.get("total_processed") or 0))
        sum_rate = sum(per_backend_rate)
        samples.append({
            "elapsed_s": elapsed,
            "agg_status": status.get("status"),
            "n_backends": len(backends),
            "per_backend_nonces_per_second": per_backend_rate,
            "per_backend_total_processed": per_backend_processed,
            "sum_nonces_per_second": sum_rate,
        })
        print(
            f"  [{elapsed:>3d}s] backends={len(backends)} "
            f"sum_rate={sum_rate:.2f} n/s ({sum_rate * 60:.0f} n/min) "
            f"per_backend={['%.2f' % x for x in per_backend_rate]}"
        )

    if not samples:
        return {"samples": [], "summary": {"avg_sum_rate": 0.0}}

    rates = [s["sum_nonces_per_second"] for s in samples]
    avg = sum(rates) / len(rates)
    return {
        "samples": samples,
        "summary": {
            "n_backends": samples[-1]["n_backends"],
            "n_samples": len(samples),
            "avg_sum_nonces_per_second": avg,
            "avg_sum_nonces_per_min": avg * 60.0,
            "min_sum_nonces_per_second": min(rates),
            "max_sum_nonces_per_second": max(rates),
            "final_per_backend_total_processed": samples[-1]["per_backend_total_processed"],
        },
    }


def validate_vectors(
    base_url: str,
    artifact: Dict[str, Any],
    stat_test: Dict[str, float],
    batch_size: Optional[int],
    timeout_s: float,
) -> Dict[str, Any]:
    """Send pre-computed vectors for validation and return the server result.

    `stat_test` is the full block sent to the server: {dist_threshold,
    p_mismatch, fraud_threshold}. The server recomputes vectors for the
    same nonces, compares each pair with L2 distance under
    `dist_threshold`, then runs a binomial fraud test using `p_mismatch`
    (per-nonce probability of a benign mismatch) and `fraud_threshold`
    (probability_honest cutoff at which to flag fraud).

    Response: {status, n_total, n_mismatch, mismatch_nonces, p_value,
    fraud_detected}.
    """
    pieces = artifact["artifacts"]
    nonces = [int(a["nonce"]) for a in pieces]
    payload = {
        "block_hash": artifact["block_hash"],
        "block_height": int(artifact.get("block_height", 100)),
        "public_key": artifact["public_key"],
        "node_id": int(artifact.get("node_id", 0)),
        "node_count": int(artifact.get("node_count", 1)),
        "nonces": nonces,
        "params": {
            "model": artifact["model"],
            "seq_len": int(artifact["seq_len"]),
            "k_dim": int(artifact["k_dim"]),
        },
        "wait": True,
        "validation": {"artifacts": pieces},
        "stat_test": dict(stat_test),
    }
    if batch_size is not None:
        payload["batch_size"] = int(batch_size)
    r = requests.post(
        f"{base_url}{API}/inference/pow/generate",
        json=payload,
        timeout=timeout_s,
    )
    if r.status_code != 200:
        raise RuntimeError(f"/inference/pow/generate failed: {r.status_code} {r.text}")
    return r.json()


# ---------------------------------------------------------------------------
# Artifact / config loading
# ---------------------------------------------------------------------------


REQUIRED_ARTIFACT_FIELDS = ("model", "seq_len", "k_dim", "block_hash", "public_key", "artifacts")


def format_stat_test(stat_test: Dict[str, float], source: Optional[Dict[str, str]] = None) -> str:
    """Render the stat_test block as 'k1=v1, k2=v2' (with provenance if given)."""
    if not stat_test:
        return "(empty; server fills all defaults)"
    if source:
        return ", ".join(f"{k}={v} ({source.get(k, '?')})" for k, v in stat_test.items())
    return ", ".join(f"{k}={v}" for k, v in stat_test.items())


def load_artifact(path: Path) -> Dict[str, Any]:
    if not path.exists():
        raise FileNotFoundError(f"artifact not found: {path}")
    data = json.loads(path.read_text(encoding="utf-8"))
    missing = [k for k in REQUIRED_ARTIFACT_FIELDS if k not in data]
    if missing:
        raise ValueError(f"artifact missing required fields: {missing}")
    if not isinstance(data["artifacts"], list) or not data["artifacts"]:
        raise ValueError("artifact 'artifacts' must be a non-empty list")
    for a in data["artifacts"]:
        if "nonce" not in a or "vector_b64" not in a:
            raise ValueError("each entry in 'artifacts' must have nonce and vector_b64")
    return data


# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------


def write_report(
    exp_dir: Path,
    reference_path: Path,
    artifact: Dict[str, Any],
    args: argparse.Namespace,
    download_info: Optional[Dict[str, Any]],
    deploy_info: Optional[Dict[str, Any]],
    throughput: Optional[Dict[str, Any]],
    validation_response: Optional[Dict[str, Any]],
    stat_test: Dict[str, float],
    stat_test_source: Dict[str, str],
    effective_additional_args: List[str],
) -> Path:
    """Write `validate_config.json`, `validate_report.json`, and `validate_report.txt`
    into the experiment directory; return the JSON report path.

    Layout matches `mlnode/packages/benchmarks/scripts/README.md`:
    one experiment = one server + one model deployment = one folder.
    """
    exp_dir.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now().isoformat()

    n_vectors = len(artifact["artifacts"])
    # Pass criterion is the binomial fraud test (fraud_detected==false). A non-zero
    # n_mismatch with fraud_detected==false is PASS-with-warning; the stat test
    # tolerates a small per-nonce mismatch rate set by p_mismatch.
    passed: Optional[bool] = None
    has_mismatches: Optional[bool] = None
    if validation_response is not None:
        n_mismatch = int(validation_response.get("n_mismatch") or 0)
        fraud_detected = bool(validation_response.get("fraud_detected", False))
        passed = not fraud_detected
        has_mismatches = n_mismatch > 0

    # ---- validate_config.json: inputs only ---------------------------------
    config = {
        "timestamp": timestamp,
        "mlnode_url": args.mlnode_url,
        "model": args.model,
        "reference": {
            "path": str(reference_path),
            "model": artifact["model"],
            "seq_len": artifact["seq_len"],
            "k_dim": artifact["k_dim"],
            "block_hash": artifact["block_hash"],
            "public_key": artifact["public_key"],
            "node_id": artifact.get("node_id", 0),
            "node_count": artifact.get("node_count", 1),
            "n_vectors": n_vectors,
            "source": artifact.get("source"),
            "additional_args": list(artifact.get("additional_args") or []),
        },
        "deploy_config": {
            "dtype": args.dtype,
            "additional_args": list(effective_additional_args),
            "source": "reference.additional_args + cli overrides (--tp-size / --max-model-len / --extra-arg add-or-update)",
        },
        "poc_params": {
            "seq_len": artifact["seq_len"],
            "k_dim": artifact["k_dim"],
            "block_hash": artifact["block_hash"],
            "public_key": artifact["public_key"],
            "node_id": artifact.get("node_id", 0),
            "node_count": artifact.get("node_count", 1),
            "throughput_batch_size": args.batch_size,
            "validation_batch_size": args.validation_batch_size,
            "source": "PoC params come from the reference JSON; batch sizes come from CLI",
        },
        "stat_test": {
            "values": dict(stat_test),
            "source": dict(stat_test_source),
        },
        "args": vars(args),
    }
    config_path = exp_dir / "validate_config.json"
    config_path.write_text(json.dumps(config, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # ---- validate_report.json: inputs + per-phase results + verdict --------
    report = dict(config)
    report["download"] = download_info
    report["deploy"] = deploy_info
    report["throughput"] = throughput
    report["validation"] = {
        "request": {"n_total": n_vectors},
        "response": validation_response,
        "passed": passed,
        "has_mismatches": has_mismatches,
    }
    json_path = exp_dir / "validate_report.json"
    json_path.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # Compute verdict up front so the report can lead with it.
    if validation_response is None:
        verdict = "validation skipped"
    elif passed is True and not has_mismatches:
        verdict = "PASS"
    elif passed is True and has_mismatches:
        verdict = "PASS (with mismatches within stat-test tolerance)"
    else:
        verdict = "FAIL (fraud_detected)"

    # Short text summary
    lines: List[str] = []
    lines.append("=" * 70)
    lines.append("MLNode PoC validation report")
    lines.append("=" * 70)
    lines.append(f"verdict:      {verdict}")
    lines.append(f"timestamp:    {timestamp}")
    lines.append(f"mlnode:       {args.mlnode_url}")
    lines.append(f"model:        {artifact['model']}")
    lines.append(f"reference:    {reference_path}")
    lines.append(f"              ({n_vectors} pre-computed nonces; "
                 f"source: {artifact.get('source') or 'unknown'})")
    lines.append(f"experiment:   {exp_dir}")
    lines.append("")
    lines.append("PoC params (from reference unless noted):")
    lines.append(
        f"  seq_len={artifact['seq_len']}  k_dim={artifact['k_dim']}  "
        f"block_hash={artifact['block_hash']!r}  public_key={artifact['public_key']!r}"
    )
    def _bs(v: Optional[int]) -> str:
        return f"{v} (cli)" if v is not None else "unset (server POC_BATCH_SIZE_DEFAULT)"
    lines.append(
        f"  node_id={artifact.get('node_id', 0)}  "
        f"node_count={artifact.get('node_count', 1)}  "
        f"throughput_batch_size={_bs(args.batch_size)}  "
        f"validation_batch_size={_bs(args.validation_batch_size)}"
    )
    lines.append("Deploy config (reference.additional_args + CLI overrides; --tp-size / --max-model-len add-or-update):")
    lines.append(f"  dtype={args.dtype}  additional_args={effective_additional_args}")
    lines.append(f"Stat test (server-default -> artifact -> cli):")
    lines.append(f"  {format_stat_test(stat_test, stat_test_source)}")
    lines.append("")

    if download_info is not None:
        last = download_info.get("last_status") or {}
        lines.append(
            f"[download] {download_info.get('action')} (elapsed "
            f"{download_info.get('elapsed_s', 0):.1f}s; final status: {last.get('status')})"
        )
    else:
        lines.append("[download] skipped")

    if deploy_info is not None:
        action = deploy_info.get("action")
        line = f"[deploy] {action} (elapsed {deploy_info.get('elapsed_s', 0):.1f}s)"
        if action == "already_running":
            line += "  (vLLM was already up; the deploy config above was NOT applied)"
        lines.append(line)
    else:
        lines.append("[deploy] skipped")

    if throughput is not None:
        s = throughput["summary"]
        lines.append(
            f"[throughput] backends={s['n_backends']}  "
            f"avg sum rate = {s['avg_sum_nonces_per_second']:.2f} nonces/s "
            f"({s['avg_sum_nonces_per_min']:.0f} nonces/min)  "
            f"min/max = {s['min_sum_nonces_per_second']:.2f}/{s['max_sum_nonces_per_second']:.2f}"
        )
        if s.get("n_backends", 0) > 1:
            samples = throughput.get("samples") or []
            if samples:
                last_per = samples[-1].get("per_backend_nonces_per_second") or []
                fmt = "[" + ", ".join(f"{x:.2f}" for x in last_per) + "]"
                lines.append(f"  per-backend nonces/s (last sample): {fmt}")
    else:
        lines.append("[throughput] skipped")

    if validation_response is not None:
        n_total = validation_response.get("n_total", n_vectors)
        n_mismatch_v = validation_response.get("n_mismatch", "?")
        p_value = validation_response.get("p_value", "?")
        fraud = validation_response.get("fraud_detected", "?")
        lines.append(
            f"[validate] n_total={n_total}  n_mismatch={n_mismatch_v}  "
            f"p_value={p_value}  fraud_detected={fraud}  -> {verdict}"
        )
        bad = validation_response.get("mismatch_nonces") or []
        if bad:
            lines.append(f"           mismatch_nonces: {bad[:20]}{' ...' if len(bad) > 20 else ''}")
    else:
        lines.append("[validate] skipped")
    lines.append("=" * 70)

    text = "\n".join(lines)
    txt_path = exp_dir / "validate_report.txt"
    txt_path.write_text(text + "\n", encoding="utf-8")

    print()
    print(text)
    print()
    print(f"experiment dir: {exp_dir}")
    print(f"  config (json): {config_path}")
    print(f"  report (json): {json_path}")
    print(f"  report (txt):  {txt_path}")
    return json_path


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="End-to-end MLNode PoC v2 validation",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    p.add_argument("--mlnode-url", required=True,
                   help="MLNode base URL under test (required, e.g. http://1.2.3.4:8080)")
    p.add_argument("--model", required=True,
                   help="Target model id to deploy and validate (required, e.g. Qwen/Qwen3-235B-A22B-Instruct-2507-FP8). "
                        "Looked up under artifacts/<sanitized>.json unless --artifact is given.")
    p.add_argument("--reference", "--artifact", dest="reference", default=None,
                   help="Optional explicit path to the golden reference JSON; overrides the model-based "
                        "lookup. The reference's 'model' field must match --model. "
                        "(Legacy alias: --artifact.)")
    p.add_argument("--threshold", type=float, default=None,
                   help="L2 dist threshold for the per-nonce mismatch test (default: "
                        "artifact.dist_threshold or 0.2)")
    p.add_argument("--p-mismatch", type=float, default=None,
                   help="Per-nonce probability of a benign mismatch under honest computation "
                        "(default: artifact.stat_test.p_mismatch or server default)")
    p.add_argument("--fraud-threshold", type=float, default=None,
                   help="probability_honest cutoff at which the server flags fraud "
                        "(default: artifact.stat_test.fraud_threshold or server default)")
    p.add_argument("--exp-name", default=None,
                   help="Experiment name prefix used when --exp-dir is auto-created. "
                        "Default: 'mlnode_validate_<sanitized model>'.")
    p.add_argument("--exp-dir", default=None,
                   help="Write the per-run report into this directory (created if missing). "
                        "Default: data/experiments/<exp-name>_<YYYY-MM-DD_HHMMSS>/ under "
                        "mlnode/packages/benchmarks/. Matches the layout in "
                        "mlnode/packages/benchmarks/scripts/README.md.")

    # download controls
    p.add_argument("--skip-download", action="store_true",
                   help="Skip the model download/verify phase. Use only when you know the model is already cached.")
    p.add_argument("--download-timeout", type=float, default=DEFAULT_DOWNLOAD_TIMEOUT_SECONDS,
                   help=f"Max seconds to wait for the model to be DOWNLOADED (default: {DEFAULT_DOWNLOAD_TIMEOUT_SECONDS})")

    # deploy controls
    p.add_argument("--skip-deploy", action="store_true", help="Assume vLLM is already running; do not POST /inference/up/async")
    p.add_argument("--dtype", default="auto", help="vLLM --dtype passed to /inference/up/async (default: auto)")
    p.add_argument("--tp-size", type=int, default=None, help="Add --tensor-parallel-size to additional_args")
    p.add_argument("--max-model-len", type=int, default=None, help="Add --max-model-len to additional_args")
    p.add_argument("--extra-arg", action="append", default=[],
                   help="Append a single token to additional_args; pass multiple times")
    p.add_argument("--deploy-timeout", type=float, default=DEFAULT_DEPLOY_TIMEOUT_SECONDS,
                   help=f"Max seconds to wait for vLLM to come up (default: {DEFAULT_DEPLOY_TIMEOUT_SECONDS})")

    # throughput controls
    p.add_argument("--skip-throughput", action="store_true", help="Skip the generation/throughput phase")
    p.add_argument("--warmup-seconds", type=int, default=DEFAULT_WARMUP_SECONDS)
    p.add_argument("--measure-seconds", type=int, default=DEFAULT_MEASURE_SECONDS)
    p.add_argument("--sample-interval", type=int, default=10)
    p.add_argument("--batch-size", type=int, default=None,
                   help="batch_size sent to /pow/init/generate for the throughput phase. "
                        "Unset means: do not send batch_size in the request, so the server uses POC_BATCH_SIZE_DEFAULT.")

    # validation controls
    p.add_argument("--skip-validate", action="store_true", help="Skip the validation phase (only deploy + throughput)")
    p.add_argument("--validation-timeout", type=float, default=DEFAULT_VALIDATION_TIMEOUT_SECONDS)
    p.add_argument("--validation-batch-size", type=int, default=None,
                   help="batch_size sent to /pow/generate. Must match generation batch size for the same numerical "
                        "trajectory. Unset means: do not send batch_size in the request, so the server uses POC_BATCH_SIZE_DEFAULT.")
    return p.parse_args(argv)


def _drop_flag(args: List[str], flag: str) -> List[str]:
    """Remove every occurrence of `flag <value>` from a vLLM arg list."""
    out: List[str] = []
    i = 0
    while i < len(args):
        if args[i] == flag and i + 1 < len(args):
            i += 2  # skip flag and its value
            continue
        out.append(args[i])
        i += 1
    return out


def build_additional_args(artifact: Dict[str, Any], args: argparse.Namespace) -> List[str]:
    """Compose effective vLLM additional_args.

    The artifact stores the consensus-default deploy args for this model
    (canonical precision, attention backend, tp-size, etc.). They are
    used as-is unless the caller explicitly asks for an override:

    - `--tp-size N` and `--max-model-len N` are add-or-update: if the
      flag is already in the artifact, its value is replaced; if not,
      the flag is appended.
    - `--extra-arg <token>` always appends unstructured tokens; the
      caller is responsible for not re-introducing flags that conflict
      with the artifact.
    """
    base: List[str] = list(artifact.get("additional_args") or [])
    if args.tp_size is not None:
        base = _drop_flag(base, "--tensor-parallel-size")
        base += ["--tensor-parallel-size", str(args.tp_size)]
    if args.max_model_len is not None:
        base = _drop_flag(base, "--max-model-len")
        base += ["--max-model-len", str(args.max_model_len)]
    base += list(args.extra_arg or [])
    return base


def main(argv: Optional[List[str]] = None) -> int:
    args = parse_args(argv)

    if args.reference:
        reference_path = Path(args.reference).resolve()
    else:
        reference_path = resolve_reference_path(args.model)

    if not reference_path.exists():
        sys.stderr.write(
            f"ERROR: no golden reference found for model {args.model!r}\n"
            f"  expected:  {reference_path}\n"
            f"  available: {sorted(p.name for p in REFERENCES_DIR.glob('*.json')) if REFERENCES_DIR.exists() else []}\n\n"
            f"  Bake one against a trusted MLNode that already serves this model:\n"
            f"    python3 {Path(__file__).parent / 'make_artifact.py'} \\\n"
            f"        --mlnode-url <trusted_mlnode_url> \\\n"
            f"        --model {args.model!r} \\\n"
            f"        --num-nonces 32 --batch-size 32 \\\n"
            f"        --out {reference_path}\n"
        )
        return 1

    artifact = load_artifact(reference_path)
    if artifact["model"] != args.model:
        sys.stderr.write(
            f"ERROR: reference model mismatch\n"
            f"  --model:         {args.model}\n"
            f"  reference.model: {artifact['model']}\n"
            f"  reference path:  {reference_path}\n"
        )
        return 1

    # Resolve stat_test triple with provenance: server-default -> artifact ->
    # CLI override (in increasing precedence). Always send all three so the
    # report shows exactly what the server tested against (the server fills
    # missing keys with its own defaults but does not echo them back).
    artifact_stat_test = dict(artifact.get("stat_test") or {})
    artifact_dist = artifact.get("dist_threshold")
    stat_test: Dict[str, float] = dict(SERVER_DEFAULT_STAT_TEST)
    stat_test_source: Dict[str, str] = {k: "server-default" for k in stat_test}

    if artifact_dist is not None:
        stat_test["dist_threshold"] = float(artifact_dist)
        stat_test_source["dist_threshold"] = "artifact"
    for k in ("p_mismatch", "fraud_threshold"):
        if k in artifact_stat_test:
            stat_test[k] = float(artifact_stat_test[k])
            stat_test_source[k] = "artifact"
    if args.threshold is not None:
        stat_test["dist_threshold"] = float(args.threshold)
        stat_test_source["dist_threshold"] = "cli"
    if args.p_mismatch is not None:
        stat_test["p_mismatch"] = float(args.p_mismatch)
        stat_test_source["p_mismatch"] = "cli"
    if args.fraud_threshold is not None:
        stat_test["fraud_threshold"] = float(args.fraud_threshold)
        stat_test_source["fraud_threshold"] = "cli"

    if args.exp_dir:
        exp_dir = Path(args.exp_dir).resolve()
    else:
        exp_name = args.exp_name or f"mlnode_validate_{model_to_safe_id(args.model)}"
        exp_dir = (EXPERIMENTS_ROOT / f"{exp_name}_{datetime.now().strftime('%Y-%m-%d_%H%M%S')}").resolve()

    base = args.mlnode_url.rstrip("/")
    effective_additional_args = build_additional_args(artifact, args)
    print(f"MLNode:           {base}")
    print(f"Model:            {args.model}")
    print(f"Reference:        {reference_path}")
    print(f"  source:         {artifact.get('source') or 'unknown'}")
    print(f"  vectors:        {len(artifact['artifacts'])} pre-computed nonces")
    print(f"Experiment dir:   {exp_dir}")
    print(f"PoC params:       seq_len={artifact['seq_len']}  k_dim={artifact['k_dim']}  "
          f"block_hash={artifact['block_hash']!r}  public_key={artifact['public_key']!r}  "
          f"node_id={artifact.get('node_id', 0)}  node_count={artifact.get('node_count', 1)}")
    print(f"Deploy config:    dtype={args.dtype}  additional_args={effective_additional_args}")
    print(f"  source:         artifact.additional_args + cli overrides")
    print(f"Stat test:        {format_stat_test(stat_test, stat_test_source)}")
    print()

    download_info: Optional[Dict[str, Any]] = None
    deploy_info: Optional[Dict[str, Any]] = None
    throughput: Optional[Dict[str, Any]] = None
    validation_response: Optional[Dict[str, Any]] = None

    try:
        # 1. Download
        if args.skip_download:
            print("[1/4] download: skipped (--skip-download)")
        else:
            print(f"[1/4] download: ensuring {artifact['model']!r} is cached on the MLNode")
            download_info = ensure_model_downloaded(
                base,
                hf_repo=artifact["model"],
                download_timeout_s=args.download_timeout,
            )
            print(f"  -> {download_info['action']} in {download_info['elapsed_s']:.1f}s "
                  f"(status: {download_info['last_status'].get('status')})")

        # 2. Deploy
        if args.skip_deploy:
            print("[2/4] deploy: skipped (--skip-deploy)")
            cur = get_inference_status(base)
            if not cur.get("is_running"):
                raise RuntimeError(f"vLLM is not running and --skip-deploy was passed: {cur}")
        else:
            print(f"[2/4] deploy: model={artifact['model']} dtype={args.dtype} additional_args={effective_additional_args}")
            deploy_info = deploy_model(
                base,
                model=artifact["model"],
                dtype=args.dtype,
                additional_args=effective_additional_args,
                deploy_timeout_s=args.deploy_timeout,
            )
            deploy_info["additional_args"] = effective_additional_args
            deploy_info["dtype"] = args.dtype
            print(f"  -> {deploy_info['action']} in {deploy_info['elapsed_s']:.1f}s")

        # 3. Throughput
        if args.skip_throughput:
            print("[3/4] throughput: skipped (--skip-throughput)")
        else:
            print("[3/4] throughput: starting init/generate")
            init_resp = init_generate(base, artifact, batch_size=args.batch_size)
            print(f"  init/generate -> backends={init_resp.get('backends')} n_groups={init_resp.get('n_groups')}")
            try:
                throughput = measure_throughput(
                    base,
                    warmup_s=args.warmup_seconds,
                    measure_s=args.measure_seconds,
                    sample_interval_s=args.sample_interval,
                )
            finally:
                print("  stopping generation...")
                stop_generation(base)
                # Give generation loop a moment to settle before validation runs
                time.sleep(2.0)

        # 4. Validate pre-computed vectors
        if args.skip_validate:
            print("[4/4] validate: skipped (--skip-validate)")
        else:
            print(f"[4/4] validate: sending {len(artifact['artifacts'])} vectors  "
                  f"stat_test: {format_stat_test(stat_test, stat_test_source)}")
            validation_response = validate_vectors(
                base,
                artifact,
                stat_test=stat_test,
                batch_size=args.validation_batch_size,
                timeout_s=args.validation_timeout,
            )
            print(
                f"  response: n_total={validation_response.get('n_total')} "
                f"n_mismatch={validation_response.get('n_mismatch')} "
                f"p_value={validation_response.get('p_value')} "
                f"fraud_detected={validation_response.get('fraud_detected')}"
            )

    except KeyboardInterrupt:
        print("\nInterrupted; stopping generation and writing partial report")
        stop_generation(base)
    except Exception as exc:
        print(f"\nERROR: {exc}")
        write_report(exp_dir, reference_path, artifact, args, download_info, deploy_info, throughput, validation_response, stat_test, stat_test_source, effective_additional_args)
        return 1

    write_report(exp_dir, reference_path, artifact, args, download_info, deploy_info, throughput, validation_response, stat_test, stat_test_source, effective_additional_args)
    if validation_response is not None:
        # PASS criterion is the stat test alone: fraud_detected must be false.
        # n_mismatch > 0 with fraud_detected==false is allowed (within tolerance)
        # and is reported as PASS-with-warning, not FAIL.
        if bool(validation_response.get("fraud_detected", False)):
            return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
