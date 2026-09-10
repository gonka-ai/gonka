"""Host-level metrics: CPU from /proc/stat, memory from cgroup, HF-cache disk.

Memory comes from the cgroup (v2 with v1 fallback), not /proc/meminfo: inside
a container meminfo shows the host, while the OOM decision is made against the
cgroup limit. No readable source => no series.
"""

import os
import shutil
import threading
from typing import Dict, List, Optional, Tuple

from common.logger import create_logger

from api.metrics import render

logger = create_logger(__name__)

PROC_STAT = "/proc/stat"
CGROUP_V2_BASE = "/sys/fs/cgroup"
CGROUP_V1_MEM_BASE = "/sys/fs/cgroup/memory"

# Previous /proc/stat sample for ratio computation: (busy, steal, total).
# collect() runs via asyncio.to_thread, so concurrent scrapes (dapi collector
# + an operator's own Prometheus) may race on it — guarded by _cpu_lock.
# Each caller's ratio covers "since whoever scraped last"; that varying-window
# semantic is acceptable for gauges polled at a fixed cadence.
_prev_cpu: Optional[Tuple[float, float, float]] = None
_cpu_lock = threading.Lock()


def _read_first_line(path: str) -> Optional[str]:
    try:
        with open(path) as f:
            return f.readline().strip()
    except OSError:
        return None


def _read_int(path: str) -> Optional[int]:
    line = _read_first_line(path)
    if line is None or line == "max":
        return None
    try:
        return int(line)
    except ValueError:
        return None


def _cpu_sample() -> Optional[Tuple[float, float, float]]:
    line = _read_first_line(PROC_STAT)
    if not line or not line.startswith("cpu "):
        return None
    fields = [float(x) for x in line.split()[1:]]
    if len(fields) < 8:
        return None
    user, nice, system, idle, iowait, irq, softirq, steal = fields[:8]
    total = sum(fields[:8])
    busy = total - idle - iowait
    return busy, steal, total


def collect_cpu() -> Dict[str, float]:
    """CPU busy/steal ratios over the interval since the previous scrape.

    The first scrape after startup yields no series (no interval yet).
    """
    global _prev_cpu
    sample = _cpu_sample()
    if sample is None:
        return {}
    result: Dict[str, float] = {}
    with _cpu_lock:
        if _prev_cpu is not None:
            d_busy = sample[0] - _prev_cpu[0]
            d_steal = sample[1] - _prev_cpu[1]
            d_total = sample[2] - _prev_cpu[2]
            if d_total > 0:
                result["mlnode_host_cpu_busy_ratio"] = max(0.0, d_busy / d_total)
                result["mlnode_host_cpu_steal_ratio"] = max(0.0, d_steal / d_total)
        _prev_cpu = sample
    return result


def collect_memory() -> Dict[str, float]:
    """Cgroup v2 (memory.current/memory.max) with a cgroup v1 fallback."""
    result: Dict[str, float] = {}
    used = _read_int(os.path.join(CGROUP_V2_BASE, "memory.current"))
    limit = _read_int(os.path.join(CGROUP_V2_BASE, "memory.max"))
    if used is None:
        used = _read_int(os.path.join(CGROUP_V1_MEM_BASE, "memory.usage_in_bytes"))
        v1_limit = _read_int(os.path.join(CGROUP_V1_MEM_BASE, "memory.limit_in_bytes"))
        # v1 reports "no limit" as a huge page-aligned number instead of "max"
        if v1_limit is not None and v1_limit < (1 << 60):
            limit = v1_limit
    if used is not None:
        result["mlnode_host_memory_used_bytes"] = float(used)
    if limit is not None:
        result["mlnode_host_memory_limit_bytes"] = float(limit)
    return result


def collect_hf_cache_disk() -> Dict[str, float]:
    hf_home = os.getenv("HF_HOME", "/root/.cache")
    try:
        usage = shutil.disk_usage(hf_home)
    except OSError:
        return {}
    return {"mlnode_host_hf_cache_free_bytes": float(usage.free)}


def collect() -> List[str]:
    lines: List[str] = []
    values: Dict[str, float] = {}
    values.update(collect_cpu())
    values.update(collect_memory())
    values.update(collect_hf_cache_disk())
    for name in sorted(values):
        lines.append(render.series(name, {}, values[name]))
    return lines


def reset():
    """Forget the previous CPU sample (tests)."""
    global _prev_cpu
    _prev_cpu = None
