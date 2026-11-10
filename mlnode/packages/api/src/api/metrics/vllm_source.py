"""Fetch, filter and relabel vLLM /metrics from backend replicas.

Only families from the allowlist pass through. The `model_name` label value is
normalized to the served model name: vLLM reports the local HF-cache path when
the model was loaded from disk, and local paths must never leave the node.
"""

import asyncio
import re
import threading

from common.logger import create_logger

import api.proxy as proxy_module
from api.metrics.allowlist import VLLM_ALLOWLIST, VLLM_DROPPED_LABELS

logger = create_logger(__name__)

# Non-greedy org group: HF encodes org/name as models--org--name and model
# names may themselves contain "--" (org names may not).
_HF_CACHE_PATH_RE = re.compile(r"models--([^/\"]+?)--([^/\"]+)")
_LABEL_RE = re.compile(r'(\w+)="((?:[^"\\]|\\.)*)"')
_SERIES_RE = re.compile(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{.*\})?\s+(.+)$")
_ORG_NAME_RE = re.compile(r"[\w.-]+/[\w.-]+")
_SUFFIXES = ("_bucket", "_sum", "_count")

SCRAPE_TIMEOUT_SECONDS = 2.5

_last_good: dict[str, tuple[list[str], float]] = {}
_last_good_sequence: dict[str, int] = {}
_cache_generation: int = -1
_last_meta: list[str] = []
_last_meta_sequence = -1
_scrape_sequence = 0
_cache_lock = threading.Lock()


def _invalidate_cache(generation: int) -> None:
    global _cache_generation, _last_meta, _last_meta_sequence
    _last_good.clear()
    _last_good_sequence.clear()
    _last_meta = []
    _last_meta_sequence = -1
    _cache_generation = generation


def normalize_model_name(value: str) -> str:
    """Map any filesystem path to a served model name.

    HF-cache layout yields `org/name`; any other absolute path (models loaded
    from a local directory) falls back to its basename — a path must never
    leave the node (invariant 5), even at the cost of losing the org prefix.
    """
    m = _HF_CACHE_PATH_RE.search(value)
    if m:
        return f"{m.group(1)}/{m.group(2)}"
    if "/" in value and not _ORG_NAME_RE.fullmatch(value):
        # absolute or relative local path: keep only the basename
        return value.rstrip("/").rsplit("/", 1)[-1]
    return value


def _family_name(series_name: str) -> str:
    if series_name in VLLM_ALLOWLIST:
        return series_name
    for suffix in _SUFFIXES:
        if series_name.endswith(suffix):
            return series_name[: -len(suffix)]
    return series_name


def _rewrite_labels(label_block: str | None, replica: str) -> str:
    labels = []
    if label_block:
        for name, value in _LABEL_RE.findall(label_block):
            if name in VLLM_DROPPED_LABELS:
                continue
            if name == "model_name":
                value = normalize_model_name(value)
            labels.append((name, value))
    labels.append(("replica", replica))
    inner = ",".join(f'{n}="{v}"' for n, v in labels)
    return "{" + inner + "}"


def filter_vllm_metrics(text: str, replica: str) -> tuple[list[str], list[str]]:
    """Reduce one replica's exposition text to the allowlist.

    Returns (meta_lines, series_lines): HELP/TYPE metadata separately, since
    it is per-family (not per-replica) and is emitted once by the caller.
    """
    meta: list[str] = []
    series: list[str] = []
    for line in text.splitlines():
        if not line.strip():
            continue
        if line.startswith("#"):
            parts = line.split(None, 3)
            if (
                len(parts) >= 3
                and parts[1] in ("HELP", "TYPE")
                and _family_name(parts[2]) in VLLM_ALLOWLIST
            ):
                meta.append(line)
            continue
        m = _SERIES_RE.match(line)
        if not m:
            continue
        name, label_block, value = m.groups()
        if name.endswith("_created"):
            continue
        if _family_name(name) not in VLLM_ALLOWLIST:
            continue
        series.append(f"{name}{_rewrite_labels(label_block, replica)} {value}")
    return meta, series


async def _scrape_replica(
    port: int, replica: str
) -> tuple[list[str], list[str]] | None:
    try:
        resp = await asyncio.wait_for(
            proxy_module.call_backend(port, "GET", "/metrics"),
            timeout=SCRAPE_TIMEOUT_SECONDS,
        )
        if resp.status_code == 200:
            return filter_vllm_metrics(resp.text, replica)
    except Exception as e:
        logger.warning(
            f"metrics scrape failed for replica {replica} (port {port}): {e}"
        )
    return None


async def collect(now: float) -> tuple[list[str], dict[str, float]]:
    """Scrape all healthy replicas concurrently; fall back to last good copies.

    Returns (exposition_lines, {replica: last_success_ts}).
    """
    global _last_meta, _last_meta_sequence, _scrape_sequence
    with _cache_lock:
        generation = getattr(proxy_module, "vllm_setup_generation", _cache_generation)
        if _cache_generation != generation:
            _invalidate_cache(generation)
        _scrape_sequence += 1
        sequence = _scrape_sequence
    all_ports = list(proxy_module.vllm_backend_ports)
    healthy = set(proxy_module.get_healthy_backends())

    targets = [
        (str(idx), port) for idx, port in enumerate(all_ports) if port in healthy
    ]
    results = await asyncio.gather(
        *(_scrape_replica(port, replica) for replica, port in targets)
    )
    with _cache_lock:
        generation_now = getattr(proxy_module, "vllm_setup_generation", generation)
        if generation_now != generation:
            if _cache_generation == generation:
                _invalidate_cache(generation_now)
            return [], {}

        for (replica, _), result in zip(targets, results):
            if result is not None and sequence > _last_good_sequence.get(replica, -1):
                meta, series = result
                _last_good[replica] = (series, now)
                _last_good_sequence[replica] = sequence
                if meta and sequence > _last_meta_sequence:
                    _last_meta = meta
                    _last_meta_sequence = sequence

        lines: list[str] = list(_last_meta)
        timestamps: dict[str, float] = {}
        for idx in range(len(all_ports)):
            replica = str(idx)
            if replica in _last_good:
                series, ts = _last_good[replica]
                lines.extend(series)
                timestamps[replica] = ts
        return lines, timestamps
