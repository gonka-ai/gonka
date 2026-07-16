"""Fetch, filter and relabel vLLM /metrics from backend replicas.

Only families from the allowlist pass through. The `model_name` label value is
normalized to the served model name: vLLM reports the local HF-cache path when
the model was loaded from disk, and local paths must never leave the node.
"""

import asyncio
import re
from typing import Dict, List, Optional, Tuple

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

# Our own timeout per replica scrape; never inherit call_backend's 60s — a
# hung replica must not stall the exporter. Sized so the worst-case handler
# response stays well under the dapi collector's 5 s per-node budget:
# a hung replica must yield stale-with-aging-timestamp, not mlnode_up 0.
SCRAPE_TIMEOUT_SECONDS = 2.5

# Last successful scrape per replica index: (series_lines, unix_ts).
# A hung vLLM keeps serving its stale copy with an aging timestamp, which is
# exactly how consumers are supposed to detect it (design invariant 4).
_last_good: Dict[str, Tuple[List[str], float]] = {}
# Proxy setup generation the cache belongs to; a mismatch in collect() means
# vLLM was (re)started — possibly with another model — and the cache is stale.
_cache_generation: int = -1
# HELP/TYPE metadata is identical across replicas; kept from the most recent
# successful scrape of any replica so it survives replica 0 being down.
_last_meta: List[str] = []


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


def _rewrite_labels(label_block: Optional[str], replica: str) -> str:
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


def filter_vllm_metrics(text: str, replica: str) -> Tuple[List[str], List[str]]:
    """Reduce one replica's exposition text to the allowlist.

    Returns (meta_lines, series_lines): HELP/TYPE metadata separately, since
    it is per-family (not per-replica) and is emitted once by the caller.
    """
    meta: List[str] = []
    series: List[str] = []
    for line in text.splitlines():
        if not line.strip():
            continue
        if line.startswith("#"):
            parts = line.split(None, 3)
            if len(parts) >= 3 and parts[1] in ("HELP", "TYPE"):
                if _family_name(parts[2]) in VLLM_ALLOWLIST:
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


async def _scrape_replica(port: int, replica: str) -> Optional[Tuple[List[str], List[str]]]:
    try:
        resp = await asyncio.wait_for(
            proxy_module.call_backend(port, "GET", "/metrics"),
            timeout=SCRAPE_TIMEOUT_SECONDS,
        )
        if resp.status_code == 200:
            return filter_vllm_metrics(resp.text, replica)
    except Exception as e:
        logger.warning(f"metrics scrape failed for replica {replica} (port {port}): {e}")
    return None


async def collect(now: float) -> Tuple[List[str], Dict[str, float]]:
    """Scrape all healthy replicas concurrently; fall back to last good copies.

    Returns (exposition_lines, {replica: last_success_ts}).
    """
    global _last_meta, _cache_generation
    # Lazy invalidation: any vLLM (re)start bumps the proxy setup generation.
    # Older proxy modules (fork images) lack the counter — invalidation is
    # then simply off, matching their pre-metrics behavior.
    generation = getattr(proxy_module, "vllm_setup_generation", _cache_generation)
    if _cache_generation != generation:
        _last_good.clear()
        _last_meta = []
        _cache_generation = generation

    generation_at_entry = _cache_generation
    all_ports = list(proxy_module.vllm_backend_ports)
    healthy = set(proxy_module.get_healthy_backends())

    targets = [
        (str(idx), port) for idx, port in enumerate(all_ports) if port in healthy
    ]
    results = await asyncio.gather(
        *(_scrape_replica(port, replica) for replica, port in targets)
    )
    # A model switch may have happened while we were suspended in gather:
    # writing these results would resurrect the previous model's series
    # right after another scraper invalidated the cache.
    generation_now = getattr(proxy_module, "vllm_setup_generation", generation_at_entry)
    if generation_now == generation_at_entry:
        for (replica, _), result in zip(targets, results):
            if result is not None:
                meta, series = result
                _last_good[replica] = (series, now)
                if meta:
                    _last_meta = meta

    lines: List[str] = list(_last_meta)
    timestamps: Dict[str, float] = {}
    for idx in range(len(all_ports)):
        replica = str(idx)
        if replica in _last_good:
            series, ts = _last_good[replica]
            lines.extend(series)
            timestamps[replica] = ts
    return lines, timestamps


def reset_cache():
    """Drop cached scrapes and metadata (called on model start/stop/switch)."""
    global _last_meta
    _last_good.clear()
    _last_meta = []
