#!/usr/bin/env python3
"""
Inference runner with mixed per-request logprobs_mode.

Loads prompts from the same SQuAD dataset as inference.py, assigns
logprobs_mode in round-robin (raw_logprobs, processed_logprobs, None),
and runs them in parallel so different modes naturally share vLLM batches.

Produces inference_results.jsonl + inference_config.json compatible with
the validation pipeline.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import asdict
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional

import requests
from pydantic import BaseModel, Field
from tqdm import tqdm


def _add_repo_paths() -> None:
    benchmarks_dir = Path(__file__).resolve().parents[2]
    sys.path.insert(0, str(benchmarks_dir / "src"))
    sys.path.insert(0, str(benchmarks_dir.parent / "common" / "src"))


_add_repo_paths()

from validation.data import ModelInfo, RequestParams, Result  # noqa: E402
from validation.prompts import (  # noqa: E402
    get_squad_data_questions,
    preload_all_language_prompts,
    slice_mixed_language_prompts_with_langs,
    DATASET_HANDLES,
)
from validation.utils import _extract_logprobs, inference  # noqa: E402


LOGPROBS_MODES = ["raw_logprobs", "processed_logprobs", None]


class InferenceArtifactItem(BaseModel):
    prompt: str
    language: str = "en"
    inference_result: Result
    inference_model: ModelInfo
    request_params: RequestParams
    metadata: Dict[str, Any] = Field(default_factory=dict)


# -- reuse helpers from inference.py ----------------------------------------

def _wait_for_vllm(base_url: str, timeout_s: int = 120) -> Dict[str, Any]:
    models_url = base_url.rstrip("/") + "/v1/models"
    deadline = time.time() + timeout_s
    last_err: Optional[str] = None
    while time.time() < deadline:
        try:
            r = requests.get(models_url, timeout=5)
            if r.status_code == 200:
                return r.json()
            last_err = f"{r.status_code}: {r.text[:200]}"
        except Exception as e:  # noqa: BLE001
            last_err = repr(e)
        time.sleep(1)
    raise RuntimeError(
        f"vLLM not ready at {models_url} within {timeout_s}s. Last: {last_err}"
    )


def _probe_vllm(base_url: str, timeout_s: int):
    from dataclasses import dataclass

    @dataclass(frozen=True)
    class VllmProbe:
        base_url: str
        models_url: str
        served_model_ids: List[str]
        raw_models_response: Dict[str, Any]
        health_status_code: Optional[int]
        version_status_code: Optional[int]
        version_body: Optional[str]
        timestamp: str

    models_json = _wait_for_vllm(base_url, timeout_s=timeout_s)
    data = models_json.get("data", [])
    served_ids = [m.get("id") for m in data if isinstance(m, dict) and m.get("id")]
    health_code = version_code = None
    version_body = None
    try:
        health_code = requests.get(
            base_url.rstrip("/") + "/health", timeout=5
        ).status_code
    except Exception:
        pass
    try:
        vr = requests.get(base_url.rstrip("/") + "/version", timeout=5)
        version_code = vr.status_code
        version_body = vr.text[:5000]
    except Exception:
        pass
    return VllmProbe(
        base_url=base_url.rstrip("/"),
        models_url=base_url.rstrip("/") + "/v1/models",
        served_model_ids=served_ids,
        raw_models_response=models_json,
        health_status_code=health_code,
        version_status_code=version_code,
        version_body=version_body,
        timestamp=datetime.now().isoformat(),
    )


def _resolve_model(configured: str, served_ids: List[str], base_url: str) -> str:
    if configured and configured in served_ids:
        return configured
    if served_ids:
        fallback = str(served_ids[0])
        if configured and configured != fallback:
            print(f"[warn] '{configured}' not in /v1/models, using '{fallback}'")
        return fallback
    if configured:
        return configured
    raise RuntimeError(f"No served models at {base_url}/v1/models")


def _run_with_retries(fn, max_attempts: int, backoff_start: float, backoff_mult: float):
    attempt, delay = 1, backoff_start
    while True:
        try:
            return fn()
        except Exception:
            if attempt >= max_attempts:
                raise
            time.sleep(delay)
            delay *= backoff_mult
            attempt += 1


def _load_prompts(
    prompts_file: Optional[Path],
    n_prompts: int,
    multilingual: bool = False,
    langs: Optional[List[str]] = None,
) -> tuple:
    if prompts_file:
        prompts = [
            t for line in prompts_file.read_text("utf-8").splitlines()
            if (t := line.strip())
        ][:n_prompts]
        return prompts, ["en"] * len(prompts)
    if multilingual:
        lang_tuple = tuple(langs) if langs else ("en", "ch", "hi", "ar")
        n_per = max(1, n_prompts // len(lang_tuple))
        by_lang = preload_all_language_prompts(lang_tuple)
        prompts, languages = slice_mixed_language_prompts_with_langs(
            by_lang, per_language_n=n_per, langs=lang_tuple
        )
        return prompts[:n_prompts], languages[:n_prompts]
    prompts = get_squad_data_questions()[:n_prompts]
    return prompts, ["en"] * len(prompts)


# ---------------------------------------------------------------------------

def main() -> None:
    parser = argparse.ArgumentParser(
        description="Inference with mixed per-request logprobs_mode (round-robin)."
    )
    parser.add_argument("--exp-name", default="mixed_logprobs")
    parser.add_argument("--exp-dir", type=Path, default=None)
    parser.add_argument("--url", required=True)
    parser.add_argument("--model", default="")
    parser.add_argument("--n-prompts", type=int, default=1000)
    parser.add_argument("--prompts-file", type=Path, default=None)
    parser.add_argument("--multilingual", action="store_true")
    parser.add_argument("--langs", type=str, nargs="*", default=None,
                        help=f"Available: {list(DATASET_HANDLES.keys())}")
    parser.add_argument("--max-workers", type=int, default=64)
    parser.add_argument("--wait-timeout-s", type=int, default=120)
    parser.add_argument("--max-attempts", type=int, default=3)
    parser.add_argument("--retry-backoff-start-s", type=float, default=1.0)
    parser.add_argument("--retry-backoff-mult", type=float, default=2.0)
    parser.add_argument("--max-tokens", type=int, default=3000)
    parser.add_argument("--temperature", type=float, default=0.99)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--top-logprobs", type=int, default=5)
    parser.add_argument("--top-p", type=float, default=None)
    parser.add_argument("--top-k", type=int, default=None)
    parser.add_argument("--repetition-penalty", type=float, default=None)
    args = parser.parse_args()

    benchmarks_dir = Path(__file__).resolve().parents[2]
    if args.exp_dir:
        exp_dir = args.exp_dir.resolve()
        exp_dir.mkdir(parents=True, exist_ok=True)
    else:
        out_base = benchmarks_dir / "data" / "experiments"
        out_base.mkdir(parents=True, exist_ok=True)
        ts = datetime.now().strftime("%Y-%m-%d_%H%M%S")
        exp_dir = out_base / f"{args.exp_name}_{ts}"
        exp_dir.mkdir(parents=True, exist_ok=True)

    artifact_path = exp_dir / "inference_results.jsonl"
    cfg_path = exp_dir / "inference_config.json"

    # --- probe & resolve model ------------------------------------------------
    probe = _probe_vllm(args.url, timeout_s=args.wait_timeout_s)
    model_name = _resolve_model(args.model or "", probe.served_model_ids, args.url)
    model_info = ModelInfo(
        url=args.url.rstrip("/") + "/",
        name=model_name,
        deploy_params={},
    )

    # --- load prompts ---------------------------------------------------------
    prompts, languages = _load_prompts(
        args.prompts_file, args.n_prompts,
        multilingual=args.multilingual, langs=args.langs,
    )

    # --- assign modes round-robin ---------------------------------------------
    modes = [LOGPROBS_MODES[i % len(LOGPROBS_MODES)] for i in range(len(prompts))]
    mode_counts = {str(m): modes.count(m) for m in LOGPROBS_MODES}
    print(f"Loaded {len(prompts)} prompts, mode distribution: {mode_counts}")

    # --- build per-mode RequestParams -----------------------------------------
    base_kw = dict(
        max_tokens=args.max_tokens,
        temperature=args.temperature,
        seed=args.seed,
        top_logprobs=args.top_logprobs,
        top_p=args.top_p,
        top_k=args.top_k,
        repetition_penalty=args.repetition_penalty,
    )
    rp_cache: dict[str | None, RequestParams] = {}
    for m in LOGPROBS_MODES:
        extra = {"logprobs_mode": m} if m is not None else {}
        rp_cache[m] = RequestParams(**base_kw, additional_params=extra)

    # --- write config (pre-run) -----------------------------------------------
    cfg: dict[str, Any] = {
        "exp_name": args.exp_name,
        "timestamp": datetime.now().isoformat(),
        "artifact_dir": str(exp_dir),
        "inference_artifact": str(artifact_path),
        "n_prompts": len(prompts),
        "multilingual": args.multilingual,
        "languages_used": sorted(set(languages)),
        "model_info": model_info.model_dump(),
        "request_params_base": base_kw,
        "logprobs_modes_used": [str(m) for m in LOGPROBS_MODES],
        "logprobs_mode_distribution": mode_counts,
        "description": (
            "Mixed per-request logprobs_mode inference. Modes assigned round-robin "
            "(raw_logprobs, processed_logprobs, None) so different modes naturally "
            "share vLLM decode batches under parallel execution."
        ),
        "vllm_runtime_probe": asdict(probe),
        "cli": vars(args),
    }
    cfg_path.write_text(json.dumps(cfg, indent=2, ensure_ascii=False, default=str) + "\n")

    # --- worker ---------------------------------------------------------------
    def _work(idx: int) -> tuple[str, int, float]:
        prompt, lang, mode = prompts[idx], languages[idx], modes[idx]
        rp = rp_cache[mode]

        def _call():
            return inference(model_info, rp, prompt)

        t0 = time.monotonic()
        resp = _run_with_retries(
            _call,
            max_attempts=max(1, args.max_attempts),
            backoff_start=args.retry_backoff_start_s,
            backoff_mult=args.retry_backoff_mult,
        )
        elapsed = time.monotonic() - t0
        result = _extract_logprobs(resp)
        n_tok = len(result.results)

        row = InferenceArtifactItem(
            prompt=prompt,
            language=lang,
            inference_result=result,
            inference_model=model_info,
            request_params=rp,
            metadata={"logprobs_mode": mode, "prompt_idx": idx},
        )
        return row.model_dump_json() + "\n", n_tok, elapsed

    # --- run ------------------------------------------------------------------
    total_tokens = 0
    prompt_times: list[float] = []
    t_start = time.monotonic()

    with artifact_path.open("w", encoding="utf-8") as f, \
         ThreadPoolExecutor(max_workers=args.max_workers) as pool:
        futures = [pool.submit(_work, i) for i in range(len(prompts))]
        for fut in tqdm(as_completed(futures), total=len(futures),
                        desc="Inference", smoothing=0):
            line, n_tok, elapsed = fut.result()
            f.write(line)
            total_tokens += n_tok
            prompt_times.append(elapsed)

    t_total = time.monotonic() - t_start

    perf = {
        "total_time_seconds": round(t_total, 3),
        "n_prompts": len(prompts),
        "total_output_tokens": total_tokens,
        "output_tokens_per_second": round(total_tokens / t_total, 2) if t_total else 0,
        "average_time_per_prompt_seconds": round(t_total / len(prompts), 3) if prompts else 0,
    }
    cfg["performance"] = perf
    cfg_path.write_text(json.dumps(cfg, indent=2, ensure_ascii=False, default=str) + "\n")

    print(f"\nDone: {len(prompts)} prompts -> {artifact_path}")
    print(f"Config -> {cfg_path}")
    print(f"Performance: {json.dumps(perf, indent=2)}")


if __name__ == "__main__":
    main()
