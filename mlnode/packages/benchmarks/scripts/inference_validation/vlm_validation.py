#!/usr/bin/env python3
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
from typing import Any, Dict, List, Optional, Tuple

import requests
from tqdm import tqdm


def _add_repo_paths() -> None:
    """Make `validation` + `common` imports resolve to package sources."""
    benchmarks_dir = Path(__file__).resolve().parents[2]
    sys.path.insert(0, str(benchmarks_dir / "src"))
    sys.path.insert(0, str(benchmarks_dir.parent / "common" / "src"))


_add_repo_paths()

from validation.data import (  # noqa: E402
    InferenceArtifactItem,
    ModelInfo,
    RequestParams,
    ValidationItem,
)
from validation.utils import (  # noqa: E402
    EnforcedTokens,
    _extract_logprobs,
    build_vlm_user_content,
    validation as validation_call,
)

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


def _load_inference_items(path: Path) -> List[InferenceArtifactItem]:
    items: List[InferenceArtifactItem] = []
    with path.open("r", encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            items.append(InferenceArtifactItem.model_validate_json(line))
    if not items:
        raise RuntimeError(f"No rows found in inference artifact: {path}")
    return items


def _collect_image_paths(images_dir: Optional[Path]) -> List[Path]:
    if images_dir is None:
        return []
    d = images_dir.expanduser().resolve()
    if not d.is_dir():
        raise NotADirectoryError(f"Not a directory: {d}")
    paths: List[Path] = []
    for f in sorted(d.rglob("*")):
        if f.is_file() and f.suffix.lower() in IMAGE_SUFFIXES:
            paths.append(f)
    if not paths:
        raise FileNotFoundError(
            f"No image files found under --images-dir: {images_dir} "
            f"(expected one of: {sorted(IMAGE_SUFFIXES)})."
        )
    return paths


def _per_item_image_lists(n_items: int, image_paths: List[Path]) -> Optional[List[List[Path]]]:
    if not image_paths:
        return None
    if len(image_paths) == 1:
        return [[image_paths[0]] for _ in range(n_items)]
    if len(image_paths) == n_items:
        return [[p] for p in image_paths]
    raise ValueError(
        f"VLM validation: need 1 image (reused for all rows) or exactly {n_items} images "
        f"(one per row); got {len(image_paths)} image(s)."
    )


def _load_inference_config(exp_dir: Path) -> Optional[Dict[str, Any]]:
    cfg_path = exp_dir / "inference_config.json"
    if not cfg_path.exists():
        return None
    try:
        return json.loads(cfg_path.read_text(encoding="utf-8"))
    except Exception as e:  # noqa: BLE001
        print(f"[warn] Failed to read inference_config.json: {e!r}")
        return None


def _extract_check_fields(inference_cfg: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    if not inference_cfg:
        return {}
    return {
        "model_info": inference_cfg.get("model_info"),
        "request_params": inference_cfg.get("request_params"),
        "vllm_runtime_probe.served_model_ids": (inference_cfg.get("vllm_runtime_probe") or {}).get("served_model_ids"),
        "vllm_runtime_probe.raw_models_response": (inference_cfg.get("vllm_runtime_probe") or {}).get("raw_models_response"),
    }


def _compare_configs(
    inference_cfg: Optional[Dict[str, Any]],
    validation_model_info: ModelInfo,
    validation_request_params: RequestParams,
    validation_probe: VllmProbe,
) -> Tuple[bool, List[str]]:
    expected = _extract_check_fields(inference_cfg)
    actual = {
        "model_info": validation_model_info.model_dump(),
        "request_params": validation_request_params.model_dump(),
        "vllm_runtime_probe.served_model_ids": validation_probe.served_model_ids,
        "vllm_runtime_probe.raw_models_response": validation_probe.raw_models_response,
    }
    diffs: List[str] = []
    for k in expected.keys():
        if expected.get(k) != actual.get(k):
            diffs.append(k)
    return len(diffs) == 0, diffs


def main() -> None:
    parser = argparse.ArgumentParser(
        description=(
            "Run VALIDATION ONLY from a pure inference artifact JSONL. "
            "Writes inference+validation artifact and validation config into the same experiment folder."
        )
    )
    parser.add_argument(
        "--inference-artifact",
        required=True,
        type=Path,
        help="Path to inference_results.jsonl (e.g. from vlm_inference.py or text inference runners).",
    )
    parser.add_argument("--validation-url", required=True, help="Server URL (mlnode API recommended for load-balancing across backends, e.g. http://HOST:8080)")
    parser.add_argument("--validation-model", default="", help="Model id to use; default: first served id from /v1/models.")
    parser.add_argument(
        "--api-key",
        default=os.environ.get("OPENAI_API_KEY", ""),
        help="Optional Bearer token (default: env OPENAI_API_KEY).",
    )
    parser.add_argument("--max-workers", type=int, default=64, help="Concurrent workers.")
    parser.add_argument("--wait-timeout-s", type=int, default=120, help="Seconds to wait for /v1/models readiness.")
    parser.add_argument("--max-attempts", type=int, default=3, help="Retry attempts per prompt.")
    parser.add_argument("--retry-backoff-start-s", type=float, default=1.0, help="Initial retry backoff in seconds.")
    parser.add_argument("--retry-backoff-mult", type=float, default=2.0, help="Retry backoff multiplier.")
    parser.add_argument(
        "--artifact-tag",
        default="",
        help="Optional suffix for output filenames, e.g. 'v09' -> validation_results__v09.jsonl",
    )
    parser.add_argument(
        "--exp-dir",
        type=Path,
        default=None,
        help="Experiment directory to write results into. Default: same directory as --inference-artifact.",
    )
    parser.add_argument(
        "--images-dir",
        type=Path,
        default=None,
        help=(
            "Optional fallback directory of images for VLM validation when inference artifact "
            "rows do not have metadata.image_paths. Supports 1 image for all rows or N images for N rows."
        ),
    )
    args = parser.parse_args()

    if not args.inference_artifact.exists():
        raise RuntimeError(f"inference artifact not found: {args.inference_artifact}")

    exp_dir = args.exp_dir.resolve() if args.exp_dir else args.inference_artifact.resolve().parent
    exp_dir.mkdir(parents=True, exist_ok=True)
    tag = f"__{args.artifact_tag}" if str(args.artifact_tag).strip() else ""
    output_path = exp_dir / f"validation_results{tag}.jsonl"
    validation_cfg_path = exp_dir / f"validation_config{tag}.json"

    inference_items = _load_inference_items(args.inference_artifact)
    fallback_images = _collect_image_paths(args.images_dir)
    per_item_images = _per_item_image_lists(len(inference_items), fallback_images)
    request_params = inference_items[0].request_params

    probe = _probe_vllm(args.validation_url, timeout_s=int(args.wait_timeout_s))
    model_name = _resolve_model_name(str(args.validation_model or ""), probe.served_model_ids, base_url=args.validation_url)
    validation_model = ModelInfo(
        url=args.validation_url.rstrip("/") + "/",
        name=model_name,
        deploy_params={},
    )

    inference_cfg = _load_inference_config(exp_dir)
    same, diff_keys = _compare_configs(inference_cfg, validation_model, request_params, probe)
    if same:
        print("[config-check] inference and validation configs look the same.")
    else:
        print("[config-check] WARNING: inference and validation configs differ (continuing):")
        for key in diff_keys:
            print(f"  - {key}")

    api_key: Optional[str] = str(args.api_key).strip() or None

    validation_cfg = {
        "timestamp": datetime.now().isoformat(),
        "artifact_dir": str(exp_dir),
        "source_inference_artifact": str(args.inference_artifact.resolve()),
        "validation_artifact": str(output_path),
        "n_items": len(inference_items),
        "validation_model_info": validation_model.model_dump(),
        "request_params": request_params.model_dump(),
        "vllm_runtime_probe": asdict(probe),
        "config_check_passed": same,
        "config_diff_keys": diff_keys,
        "cli": {
            "validation_url": args.validation_url,
            "validation_model": args.validation_model,
            "max_workers": args.max_workers,
            "wait_timeout_s": args.wait_timeout_s,
            "max_attempts": args.max_attempts,
            "retry_backoff_start_s": args.retry_backoff_start_s,
            "retry_backoff_mult": args.retry_backoff_mult,
            "artifact_tag": args.artifact_tag,
            "api_key_set": bool(api_key),
            "images_dir": str(args.images_dir.resolve()) if args.images_dir else None,
            "fallback_images_count": len(fallback_images),
        },
    }
    validation_cfg_path.write_text(json.dumps(validation_cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    def _messages_for_item(item: InferenceArtifactItem, fallback_row_images: Optional[List[Path]]) -> Optional[List[Dict[str, Any]]]:
        raw_paths = item.metadata.get("image_paths")
        image_paths: Optional[List[Path]]
        if raw_paths:
            image_paths = [Path(str(p)) for p in raw_paths]
        else:
            image_paths = fallback_row_images
        if not image_paths:
            return None
        img_detail = item.metadata.get("image_detail")
        content = build_vlm_user_content(
            text=item.prompt,
            image_paths=image_paths,
            image_detail=str(img_detail) if img_detail else None,
        )
        return [{"role": "user", "content": content}]

    def _work(item: InferenceArtifactItem, fallback_row_images: Optional[List[Path]]) -> tuple:
        enforced_tokens = EnforcedTokens.from_result(item.inference_result)
        vlm_messages = _messages_for_item(item, fallback_row_images)

        def _call():
            return validation_call(
                validation_model,
                request_params,
                item.prompt,
                enforced_tokens=enforced_tokens,
                messages=vlm_messages,
                api_key=api_key,
            )

        t0 = time.monotonic()
        resp = _run_with_retries(
            _call,
            max_attempts=max(1, int(args.max_attempts)),
            backoff_start_s=float(args.retry_backoff_start_s),
            backoff_mult=float(args.retry_backoff_mult),
        )
        prompt_elapsed = time.monotonic() - t0
        validation_result = _extract_logprobs(resp)
        n_tokens = len(validation_result.results)
        if validation_result.text != item.inference_result.text:
            print("[warn] validation text mismatch for one prompt; keeping row in artifact.")

        out_item = ValidationItem(
            prompt=item.prompt,
            language=item.language,
            inference_result=item.inference_result,
            validation_result=validation_result,
            inference_model=item.inference_model,
            validation_model=validation_model,
            request_params=request_params,
        )
        return out_item.model_dump_json() + "\n", n_tokens, prompt_elapsed

    total_output_tokens = 0
    prompt_times: List[float] = []
    run_start = time.monotonic()

    with output_path.open("w", encoding="utf-8") as f, ThreadPoolExecutor(max_workers=int(args.max_workers)) as ex:
        if per_item_images is None:
            futures = [ex.submit(_work, item, None) for item in inference_items]
        else:
            futures = [ex.submit(_work, item, per_item_images[i]) for i, item in enumerate(inference_items)]
        for fut in tqdm(as_completed(futures), total=len(futures), desc="Validation", smoothing=0):
            line, n_tok, elapsed = fut.result()
            f.write(line)
            total_output_tokens += n_tok
            prompt_times.append(elapsed)

    run_elapsed = time.monotonic() - run_start

    performance = {
        "total_time_seconds": round(run_elapsed, 3),
        "n_prompts": len(inference_items),
        "total_output_tokens": total_output_tokens,
        "output_tokens_per_second": round(total_output_tokens / run_elapsed, 2) if run_elapsed > 0 else 0,
        "average_time_per_prompt_seconds": round(run_elapsed / len(inference_items), 3) if inference_items else 0,
    }
    validation_cfg["performance"] = performance
    validation_cfg_path.write_text(json.dumps(validation_cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    print(f"done: wrote {len(inference_items)} validated rows -> {output_path}")
    print(f"config -> {validation_cfg_path}")
    print(f"performance: {json.dumps(performance, indent=2)}")


if __name__ == "__main__":
    main()