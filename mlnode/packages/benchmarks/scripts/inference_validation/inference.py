#!/usr/bin/env python3
"""
Inference-only runner for OpenAI-compatible vLLM endpoints.

Multilingual mixed run template (uses script defaults for sampling/retry/workers):
    python scripts/inference_validation/inference.py \
      --exp-name <experiment_name> \
      --url <vllm_base_url> \
      --model <served_model_id> \
      --n-prompts 1000 \
      --multilingual \
      --langs en ch hi ar sp

Notes:
- Keep `--multilingual --langs ...` to force mixed-language prompts.
- Keep `--n-prompts` as desired total (for 5 langs and 1000 prompts => 200/lang).
- Do not pass sampling flags (`--temperature`, `--top-p`, `--top-k`,
  `--repetition-penalty`) if you want pure script defaults.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import asdict, dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional

import requests
from pydantic import BaseModel, Field
from tqdm import tqdm


def _add_repo_paths() -> None:
    """Make `validation` + `common` imports work when executed as a script."""
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


class InferenceArtifactItem(BaseModel):
    prompt: str
    language: str = "en"
    inference_result: Result
    inference_model: ModelInfo
    request_params: RequestParams
    metadata: Dict[str, Any] = Field(default_factory=dict)


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
    raise RuntimeError(f"vLLM not ready at {models_url} within {timeout_s}s. Last error: {last_err}")


def _probe_vllm(base_url: str, timeout_s: int) -> VllmProbe:
    models_json = _wait_for_vllm(base_url, timeout_s=timeout_s)
    data = models_json.get("data", [])
    served_ids = [m.get("id") for m in data if isinstance(m, dict) and m.get("id")]

    health_code: Optional[int] = None
    version_code: Optional[int] = None
    version_body: Optional[str] = None

    try:
        health_code = requests.get(base_url.rstrip("/") + "/health", timeout=5).status_code
    except Exception:  # noqa: BLE001
        health_code = None

    try:
        vr = requests.get(base_url.rstrip("/") + "/version", timeout=5)
        version_code = vr.status_code
        version_body = vr.text[:5000]
    except Exception:  # noqa: BLE001
        version_code = None
        version_body = None

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


def _resolve_model_name(configured: str, served_ids: List[str], *, base_url: str) -> str:
    if configured and configured in served_ids:
        return configured
    if served_ids:
        fallback = str(served_ids[0])
        if configured and configured != fallback:
            print(
                f"[warn] Model '{configured}' not found in /v1/models for {base_url}. "
                f"Falling back to served id '{fallback}'."
            )
        return fallback
    if configured:
        return configured
    raise RuntimeError(f"No served models found at {base_url}/v1/models")


def _make_exp_dir(out_base: Path, exp_name: str) -> Path:
    out_base.mkdir(parents=True, exist_ok=True)
    ts = datetime.now().strftime("%Y-%m-%d_%H%M%S")
    exp_dir = out_base / f"{exp_name}__{ts}"
    exp_dir.mkdir(parents=True, exist_ok=True)
    return exp_dir


def _load_prompts(
    prompts_file: Optional[Path],
    n_prompts: int,
    multilingual: bool = False,
    langs: Optional[List[str]] = None,
) -> tuple:
    """Return (prompts, languages) where languages is a list of lang codes per prompt."""
    if prompts_file:
        prompts: List[str] = []
        for line in prompts_file.read_text(encoding="utf-8").splitlines():
            t = line.strip()
            if t:
                prompts.append(t)
        if not prompts:
            raise RuntimeError(f"No prompts found in file: {prompts_file}")
        prompts = prompts[:n_prompts]
        return prompts, ["en"] * len(prompts)

    if multilingual:
        lang_tuple = tuple(langs) if langs else ("en", "ch", "hi", "ar")
        n_per_lang = max(1, n_prompts // len(lang_tuple))
        all_prompts_by_lang = preload_all_language_prompts(lang_tuple)
        prompts, languages = slice_mixed_language_prompts_with_langs(
            all_prompts_by_lang, per_language_n=n_per_lang, langs=lang_tuple
        )
        return prompts[:n_prompts], languages[:n_prompts]

    prompts = get_squad_data_questions()[:n_prompts]
    return prompts, ["en"] * len(prompts)


def _run_with_retries(fn, max_attempts: int, backoff_start_s: float, backoff_mult: float):
    attempt = 1
    delay = backoff_start_s
    while True:
        try:
            return fn()
        except Exception:
            if attempt >= max_attempts:
                raise
            time.sleep(delay)
            delay *= backoff_mult
            attempt += 1


def main() -> None:
    parser = argparse.ArgumentParser(
        description=(
            "Run INFERENCE ONLY against an already running OpenAI-compatible vLLM server. "
            "Saves a pure inference artifact and inference config under data/inference_validation/<exp>__<timestamp>/."
        )
    )
    parser.add_argument("--exp-name", required=True, help="Experiment name (required).")
    parser.add_argument("--url", required=True, help="vLLM base URL, e.g. http://HOST:8000")
    parser.add_argument("--model", default="", help="Model id to use; default: first served id from /v1/models.")
    parser.add_argument("--n-prompts", type=int, default=1000, help="Number of prompts to run.")
    parser.add_argument("--prompts-file", type=Path, default=None, help="Optional text file with one prompt per line.")
    parser.add_argument("--language", default="en", help="Language tag to store in artifact rows (single-language mode).")
    parser.add_argument("--multilingual", action="store_true", help="Use multilingual Alpaca prompts (en, ch, hi, ar by default).")
    parser.add_argument("--langs", type=str, nargs="*", default=None,
                        help=f"Languages to include with --multilingual. Available: {list(DATASET_HANDLES.keys())}")
    parser.add_argument("--max-workers", type=int, default=64, help="Concurrent workers.")
    parser.add_argument("--wait-timeout-s", type=int, default=120, help="Seconds to wait for /v1/models readiness.")
    parser.add_argument("--max-attempts", type=int, default=3, help="Retry attempts per prompt.")
    parser.add_argument("--retry-backoff-start-s", type=float, default=1.0, help="Initial retry backoff in seconds.")
    parser.add_argument("--retry-backoff-mult", type=float, default=2.0, help="Retry backoff multiplier.")
    parser.add_argument("--max-tokens", type=int, default=3000)
    parser.add_argument("--temperature", type=float, default=0.99)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--top-logprobs", type=int, default=5)
    parser.add_argument("--top-p", type=float, default=None, help="Nucleus sampling top-p (omitted from payload when None).")
    parser.add_argument("--top-k", type=int, default=None, help="Top-k sampling (omitted from payload when None).")
    parser.add_argument("--repetition-penalty", type=float, default=None, help="Repetition penalty (omitted from payload when None).")
    args = parser.parse_args()

    benchmarks_dir = Path(__file__).resolve().parents[2]
    out_base = benchmarks_dir / "data" / "inference_validation"
    exp_dir = _make_exp_dir(out_base=out_base, exp_name=args.exp_name)
    inference_artifact_path = exp_dir / "inference_results.jsonl"
    inference_cfg_path = exp_dir / "inference_config.json"

    probe = _probe_vllm(args.url, timeout_s=int(args.wait_timeout_s))
    model_name = _resolve_model_name(str(args.model or ""), probe.served_model_ids, base_url=args.url)

    model_info = ModelInfo(
        url=args.url.rstrip("/") + "/",
        name=model_name,
        deploy_params={},
    )
    request_params = RequestParams(
        max_tokens=int(args.max_tokens),
        temperature=float(args.temperature),
        seed=int(args.seed),
        top_logprobs=int(args.top_logprobs),
        top_p=args.top_p,
        top_k=args.top_k,
        repetition_penalty=args.repetition_penalty,
        additional_params={},
    )

    prompts, languages = _load_prompts(
        args.prompts_file,
        n_prompts=int(args.n_prompts),
        multilingual=args.multilingual,
        langs=args.langs,
    )

    cfg = {
        "exp_name": str(args.exp_name),
        "timestamp": datetime.now().isoformat(),
        "artifact_dir": str(exp_dir),
        "inference_artifact": str(inference_artifact_path),
        "n_prompts": len(prompts),
        "multilingual": args.multilingual,
        "languages_used": sorted(set(languages)),
        "model_info": model_info.model_dump(),
        "request_params": request_params.model_dump(),
        "vllm_runtime_probe": asdict(probe),
        "cli": {
            "url": args.url,
            "model": args.model,
            "n_prompts": args.n_prompts,
            "prompts_file": str(args.prompts_file) if args.prompts_file else None,
            "max_workers": args.max_workers,
            "wait_timeout_s": args.wait_timeout_s,
            "max_attempts": args.max_attempts,
            "retry_backoff_start_s": args.retry_backoff_start_s,
            "retry_backoff_mult": args.retry_backoff_mult,
            "max_tokens": args.max_tokens,
            "temperature": args.temperature,
            "seed": args.seed,
            "top_logprobs": args.top_logprobs,
            "top_p": args.top_p,
            "top_k": args.top_k,
            "repetition_penalty": args.repetition_penalty,
        },
    }
    inference_cfg_path.write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    def _work(prompt: str, lang: str) -> tuple:
        def _call():
            return inference(model_info, request_params, prompt)

        t0 = time.monotonic()
        resp = _run_with_retries(
            _call,
            max_attempts=max(1, int(args.max_attempts)),
            backoff_start_s=float(args.retry_backoff_start_s),
            backoff_mult=float(args.retry_backoff_mult),
        )
        prompt_elapsed = time.monotonic() - t0
        inference_result = _extract_logprobs(resp)
        n_tokens = len(inference_result.results)
        row = InferenceArtifactItem(
            prompt=prompt,
            language=lang,
            inference_result=inference_result,
            inference_model=model_info,
            request_params=request_params,
            metadata={},
        )
        return row.model_dump_json() + "\n", n_tokens, prompt_elapsed

    total_output_tokens = 0
    prompt_times: List[float] = []
    run_start = time.monotonic()

    with inference_artifact_path.open("w", encoding="utf-8") as f, ThreadPoolExecutor(
        max_workers=int(args.max_workers)
    ) as ex:
        futures = [ex.submit(_work, prompt, lang) for prompt, lang in zip(prompts, languages)]
        for fut in tqdm(as_completed(futures), total=len(futures), desc="Inference", smoothing=0):
            line, n_tok, elapsed = fut.result()
            f.write(line)
            total_output_tokens += n_tok
            prompt_times.append(elapsed)

    run_elapsed = time.monotonic() - run_start

    performance = {
        "total_time_seconds": round(run_elapsed, 3),
        "n_prompts": len(prompts),
        "total_output_tokens": total_output_tokens,
        "output_tokens_per_second": round(total_output_tokens / run_elapsed, 2) if run_elapsed > 0 else 0,
        "average_time_per_prompt_seconds": round(run_elapsed / len(prompts), 3) if prompts else 0,
    }
    cfg["performance"] = performance
    inference_cfg_path.write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    print(f"done: wrote {len(prompts)} inference rows -> {inference_artifact_path}")
    print(f"config -> {inference_cfg_path}")
    print(f"performance: {json.dumps(performance, indent=2)}")


if __name__ == "__main__":
    main()
