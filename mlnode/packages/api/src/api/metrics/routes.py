"""Schema-v1 node metrics exporter.

GONKA_METRICS=off returns 404. Output is restricted to the schema allowlist
and excludes placement details.
"""

import asyncio
import os
import time
from typing import List

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import PlainTextResponse

from common.logger import create_logger

import api.proxy as proxy_module
from api.metrics import host_source, vllm_source, xid_source
from api.metrics.allowlist import SCHEMA_VERSION
from api.metrics import render
from api.metrics.render import series as _series
from api.version import get_mlnode_version

logger = create_logger(__name__)

router = APIRouter()

CONTENT_TYPE = "text/plain; version=0.0.4; charset=utf-8"

# Keep the sequential stages within dapi's 5 s per-node budget.
NVML_TIMEOUT_SECONDS = 1.0
HOST_TIMEOUT_SECONDS = 1.0


# A pending task prevents repeated workers for the same stalled source.
_pending_stages: dict = {}
_warned_stalled: set = set()


async def _bounded(coro, timeout: float, source: str):
    """Bound a stage without cancelling its possibly stalled worker."""
    previous = _pending_stages.get(source)
    if previous is not None:
        if not previous.done():
            coro.close()  # skip path: the incoming coroutine is never awaited
            if source not in _warned_stalled:
                logger.warning(f"{source} collection still stalled, skipping until it returns")
                _warned_stalled.add(source)
            raise TimeoutError(f"{source} collection stalled")
        _pending_stages.pop(source, None)
        _warned_stalled.discard(source)

    task = asyncio.ensure_future(coro)
    _pending_stages[source] = task
    task.add_done_callback(lambda t: t.exception() if not t.cancelled() else None)
    done, _ = await asyncio.wait({task}, timeout=timeout)
    if not done:
        raise TimeoutError(f"{source} collection exceeded {timeout}s")
    if _pending_stages.get(source) is task:
        _pending_stages.pop(source, None)
    return task.result()


_warned_bad_value = False
_warned_config_info = False


def metrics_enabled() -> bool:
    global _warned_bad_value
    value = os.getenv("GONKA_METRICS", "full").strip().lower()
    if value not in ("full", "off") and not _warned_bad_value:
        logger.warning(
            f"unrecognized GONKA_METRICS={value!r}, treating as 'full' (valid: full|off)"
        )
        _warned_bad_value = True
    return value != "off"


def _config_info_lines(request: Request) -> List[str]:
    manager = getattr(request.app.state, "inference_manager", None)
    runner = getattr(manager, "vllm_runner", None) if manager else None
    if runner is None:
        return []
    summary = runner.get_config_summary()
    labels = {
        "model_name": vllm_source.normalize_model_name(summary["model"]),
        "dtype": summary["dtype"],
        "replicas": str(len(proxy_module.vllm_backend_ports)),
    }
    # Empty label value = the argument was not passed explicitly and the
    # effective vLLM default is not known to mlnode (documented in schema)
    for label in ("max_num_seqs", "max_model_len",
                  "tensor_parallel_size", "pipeline_parallel_size"):
        value = summary[label]
        labels[label] = str(value) if value else ""
    return [_series("mlnode_config_info", labels, 1)]


def _version_info_lines() -> List[str]:
    return [
        _series("mlnode_version_info", {"version": get_mlnode_version()}, 1)
    ]


@router.get("/metrics")
async def get_metrics(request: Request):
    if not metrics_enabled():
        raise HTTPException(status_code=404, detail="Not Found")

    now = time.time()

    try:
        vllm_lines, vllm_timestamps = await vllm_source.collect(now)
    except Exception as e:
        logger.warning(f"vLLM metrics collection failed: {e}")
        vllm_lines, vllm_timestamps = [], {}

    # Exporter-owned series; regrouped by family with HELP/TYPE at the end
    # (vLLM passthrough above carries its own metadata).
    own: List[str] = [
        _series("gonka_metrics_schema_info", {"version": SCHEMA_VERSION}, 1)
    ]
    for replica, ts in sorted(vllm_timestamps.items()):
        own.append(
            _series(
                "mlnode_source_scrape_timestamp_seconds",
                {"source": "vllm", "replica": replica},
                ts,
            )
        )

    gpu_manager = getattr(request.app.state, "gpu_manager", None)
    if gpu_manager is not None:
        try:
            samples = await _bounded(
                gpu_manager.collect_metrics_async(), NVML_TIMEOUT_SECONDS, "NVML"
            )
        except Exception as e:
            logger.warning(f"NVML metrics collection failed: {e}")
            samples = []
        for sample in samples:
            labels = {"gpu_index": sample["gpu_index"], "gpu_model": sample["gpu_model"]}
            for name, value in sorted(sample["fields"].items()):
                own.append(_series(name, labels, value))
        if samples:
            own.append(
                _series("mlnode_source_scrape_timestamp_seconds", {"source": "nvml"}, now)
            )

    own.extend(xid_source.collect())

    try:
        # /proc reads + disk_usage are blocking I/O; a stalled HF-cache mount
        # must not freeze the event loop that also serves inference traffic
        host_lines = await _bounded(
            asyncio.to_thread(host_source.collect), HOST_TIMEOUT_SECONDS, "host"
        )
    except Exception as e:
        logger.warning(f"host metrics collection failed: {e}")
        host_lines = []
    own.extend(host_lines)
    if host_lines:
        own.append(
            _series("mlnode_source_scrape_timestamp_seconds", {"source": "host"}, now)
        )

    global _warned_config_info
    try:
        own.extend(_config_info_lines(request))
    except Exception as e:
        # warn once: on fork images whose runner lacks the accessor this is
        # a permanent condition, not a per-scrape event
        if not _warned_config_info:
            logger.warning(f"config info collection failed: {e}")
            _warned_config_info = True
    own.extend(_version_info_lines())

    lines = vllm_lines + render.grouped_with_meta(own)
    return PlainTextResponse("\n".join(lines) + "\n", media_type=CONTENT_TYPE)
