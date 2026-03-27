#!/usr/bin/env python3
"""
Inference-only runner for OpenAI-compatible endpoints.

The --url should point to the mlnode API (port 8080), which proxies /v1/*
requests to vLLM backends with least-connections load-balancing. This ensures
all backends are utilised. Pointing directly at a single vLLM backend port
(e.g. 5001) also works but bypasses load-balancing.

Multilingual mixed run template (uses script defaults for sampling/retry/workers):
    python vlm_inference.py \\
      --exp-name <experiment_name> \\
      --url http://<HOST>:<API_PORT> \\
      --model <served_model_id> \\
      --n-prompts 1000 \\
      --multilingual \\
      --langs en ch hi ar sp

Vision (same row layout as text; add images via --image / --images-dir):
    python vlm_inference.py \\
      --exp-name vlm_run \\
      --url http://<HOST>:<API_PORT> \\
      --model <vlm_id> \\
      --n-prompts 10 \\
      --images-dir ./imgs \\
      --prompt "Describe the image."

Notes:
- Keep `--multilingual --langs ...` to force mixed-language prompts.
- Keep `--n-prompts` as desired total (for 5 langs and 1000 prompts => 200/lang).
- Do not pass sampling flags (`--temperature`, `--top-p`, `--top-k`,
  `--repetition-penalty`) if you want pure script defaults.
- With VLM: either one image (reused for every prompt) or as many images as prompts.
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
from typing import Any, Dict, List, Optional, Sequence, Tuple

import requests
from tqdm import tqdm


def _add_repo_paths() -> None:
    """Make `validation` + `common` imports resolve to package sources."""
    benchmarks_dir = Path(__file__).resolve().parents[2]
    sys.path.insert(0, str(benchmarks_dir / "src"))
    sys.path.insert(0, str(benchmarks_dir.parent / "common" / "src"))


_add_repo_paths()

from validation.data import InferenceArtifactItem, ModelInfo, RequestParams  # noqa: E402
from validation.utils import _extract_logprobs, build_vlm_user_content, inference  # noqa: E402

try:
    from validation.prompts import (  # type: ignore[import-untyped]
        DATASET_HANDLES,
        get_squad_data_questions,
        preload_all_language_prompts,
        slice_mixed_language_prompts_with_langs,
    )
except ImportError:
    DATASET_HANDLES: Dict[str, str] = {}

    def get_squad_data_questions() -> List[str]:
        return list(_FALLBACK_SQUAD_QUESTIONS)

    def preload_all_language_prompts(lang_tuple: Tuple[str, ...]) -> dict:
        raise RuntimeError(
            "Multilingual prompts require the `validation.prompts` package. "
            "Use --prompts-file or install the validation package."
        )

    def slice_mixed_language_prompts_with_langs(*_a: Any, **_k: Any) -> Tuple[List[str], List[str]]:
        raise RuntimeError(
            "Multilingual prompts require the `validation.prompts` package. "
            "Use --prompts-file or install the validation package."
        )


# Used only when `validation.prompts` is not available (same role as Squad list).
_FALLBACK_SQUAD_QUESTIONS = [
    "What information is relevant to answer the question?",
    "What is the main claim in the passage?",
    "Which detail supports the conclusion?",
] * 400


IMAGE_SUFFIXES = {".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp"}


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
    exp_dir = out_base / f"{exp_name}_{ts}"
    exp_dir.mkdir(parents=True, exist_ok=True)
    return exp_dir


def _collect_image_paths(images_dir: Optional[Path], image_args: List[Path]) -> List[Path]:
    paths: List[Path] = []
    for p in image_args:
        rp = p.expanduser().resolve()
        if not rp.is_file():
            raise FileNotFoundError(f"Image not found: {rp}")
        paths.append(rp)

    if images_dir is not None:
        d = images_dir.expanduser().resolve()
        if not d.is_dir():
            raise NotADirectoryError(f"Not a directory: {d}")
        for f in sorted(d.rglob("*")):
            if f.is_file() and f.suffix.lower() in IMAGE_SUFFIXES:
                paths.append(f)

    seen: set[str] = set()
    unique: List[Path] = []
    for p in paths:
        key = str(p.resolve())
        if key not in seen:
            seen.add(key)
            unique.append(p)

    if images_dir is not None and not unique and not image_args:
        raise FileNotFoundError(
            f"No image files found under --images-dir: {images_dir} "
            f"(expected one of: {sorted(IMAGE_SUFFIXES)})."
        )
    return unique


def _per_prompt_image_lists(
    n_prompts: int,
    image_paths: Sequence[Path],
) -> Optional[List[List[Path]]]:
    """One list of image paths per prompt row. None = text-only (original behaviour)."""
    if not image_paths:
        return None
    imgs = list(image_paths)
    if len(imgs) == 1:
        return [[imgs[0]] for _ in range(n_prompts)]
    if len(imgs) == n_prompts:
        return [[p] for p in imgs]
    raise ValueError(
        f"VLM: need 1 image (repeated for all prompts) or exactly {n_prompts} images "
        f"(one per prompt); got {len(imgs)} image(s)."
    )


def _load_prompts(
    prompts_file: Optional[Path],
    n_prompts: int,
    multilingual: bool = False,
    langs: Optional[List[str]] = None,
    prompt_text: Optional[str] = None,
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

    if prompt_text:
        return [prompt_text] * n_prompts, ["en"] * n_prompts

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
            "Saves a pure inference artifact and inference config under data/experiments/<exp>_<timestamp>/. "
            "With images, each row uses the same protocol as text inference (utils.inference + _extract_logprobs)."
        )
    )
    parser.add_argument("--exp-name", default="inference", help="Experiment name prefix (used when --exp-dir is not set).")
    parser.add_argument(
        "--exp-dir",
        type=Path,
        default=None,
        help="Write into an existing experiment directory instead of creating a new one.",
    )
    parser.add_argument("--url", required=True, help="Server URL (mlnode API recommended for load-balancing across backends, e.g. http://HOST:8080)")
    parser.add_argument("--model", default="", help="Model id to use; default: first served id from /v1/models.")
    parser.add_argument(
        "--api-key",
        default=os.environ.get("OPENAI_API_KEY", ""),
        help="Optional Bearer token (default: env OPENAI_API_KEY).",
    )
    parser.add_argument("--n-prompts", type=int, default=1000, help="Number of prompts to run.")
    parser.add_argument(
        "--prompt",
        type=str,
        default=None,
        help="Single prompt text repeated for all requests (useful for VLM image batches).",
    )
    parser.add_argument("--prompts-file", type=Path, default=None, help="Optional text file with one prompt per line.")
    parser.add_argument("--language", default="en", help="Language tag to store in artifact rows (single-language mode).")
    parser.add_argument("--multilingual", action="store_true", help="Use multilingual Alpaca prompts (en, ch, hi, ar by default).")
    langs_help = f"Languages to include with --multilingual. Available: {list(DATASET_HANDLES.keys())}" if DATASET_HANDLES else "Languages to include with --multilingual."
    parser.add_argument("--langs", type=str, nargs="*", default=None, help=langs_help)
    parser.add_argument(
        "--image",
        type=Path,
        action="append",
        default=[],
        dest="images",
        help="Image file (repeatable). With VLM: one image for all prompts, or one per prompt (see --n-prompts).",
    )
    parser.add_argument(
        "--images-dir",
        type=Path,
        default=None,
        help="Directory of images (jpg/png/webp/...). Combined with --image; sorted order.",
    )
    parser.add_argument(
        "--image-detail",
        choices=("auto", "low", "high"),
        default=None,
        help="Optional image_url.detail for multimodal requests.",
    )
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
    if args.exp_dir:
        exp_dir = args.exp_dir.resolve()
        exp_dir.mkdir(parents=True, exist_ok=True)
    else:
        out_base = benchmarks_dir / "data" / "experiments"
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
        prompt_text=args.prompt,
    )

    image_paths_raw = _collect_image_paths(args.images_dir, list(args.images))
    try:
        per_prompt_images = _per_prompt_image_lists(len(prompts), image_paths_raw)
    except ValueError as e:
        raise SystemExit(str(e)) from e

    api_key: Optional[str] = str(args.api_key).strip() or None
    image_detail: Optional[str] = args.image_detail

    cfg = {
        "exp_name": str(args.exp_name),
        "timestamp": datetime.now().isoformat(),
        "artifact_dir": str(exp_dir),
        "inference_artifact": str(inference_artifact_path),
        "n_prompts": len(prompts),
        "multilingual": args.multilingual,
        "languages_used": sorted(set(languages)),
        "vlm": per_prompt_images is not None,
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
            "images": [str(p) for p in image_paths_raw],
            "image_detail": image_detail,
        },
    }
    inference_cfg_path.write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    def _work(
        prompt: str,
        lang: str,
        images_for_row: Optional[List[Path]],
    ) -> tuple:
        def _call():
            if images_for_row:
                content = build_vlm_user_content(
                    text=prompt,
                    image_paths=images_for_row,
                    image_detail=image_detail,
                )
                messages = [{"role": "user", "content": content}]
                return inference(model_info, request_params, prompt, messages=messages, api_key=api_key)
            return inference(model_info, request_params, prompt, api_key=api_key)

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
        meta: Dict[str, Any] = {}
        if images_for_row:
            meta["image_paths"] = [str(p) for p in images_for_row]
            if image_detail:
                meta["image_detail"] = image_detail
        row = InferenceArtifactItem(
            prompt=prompt,
            language=lang,
            inference_result=inference_result,
            inference_model=model_info,
            request_params=request_params,
            metadata=meta,
        )
        return row.model_dump_json() + "\n", n_tokens, prompt_elapsed

    total_output_tokens = 0
    prompt_times: List[float] = []
    run_start = time.monotonic()

    with inference_artifact_path.open("w", encoding="utf-8") as f, ThreadPoolExecutor(
        max_workers=int(args.max_workers)
    ) as ex:
        if per_prompt_images is None:
            futures = [ex.submit(_work, prompt, lang, None) for prompt, lang in zip(prompts, languages)]
        else:
            futures = [
                ex.submit(_work, prompt, lang, per_prompt_images[i])
                for i, (prompt, lang) in enumerate(zip(prompts, languages))
            ]
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
