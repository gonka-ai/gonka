"""Shared Prometheus text-exposition rendering (single escaping authority)."""

from typing import Dict

from common.logger import create_logger

logger = create_logger(__name__)


def escape(value: str) -> str:
    # backslash first; the text format requires \n escaping in label values —
    # one raw newline would corrupt the node's whole exposition downstream
    return value.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def series(name: str, labels: Dict[str, str], value: float) -> str:
    if labels:
        inner = ",".join(f'{k}="{escape(str(v))}"' for k, v in labels.items())
        return f"{name}{{{inner}}} {value}"
    return f"{name} {value}"


# Metadata for every exporter-owned family (schema v1). The dapi federation
# re-exports what we emit: without TYPE these all degrade to "untyped" and
# counters lose counter semantics in strict consumers.
FAMILY_META = {
    "gonka_metrics_schema_info": ("gauge", "Metrics schema version exported by this node."),
    "mlnode_version_info": ("gauge", "mlnode API package version."),
    "mlnode_config_info": ("gauge", "Effective inference configuration of this node."),
    "mlnode_source_scrape_timestamp_seconds": ("gauge", "Unix time of the last successful collection per source."),
    "mlnode_gpu_temp_core_celsius": ("gauge", "GPU core temperature."),
    "mlnode_gpu_temp_hbm_celsius": ("gauge", "GPU HBM memory temperature (absent on GDDR cards)."),
    "mlnode_gpu_power_draw_watts": ("gauge", "Current GPU power draw."),
    "mlnode_gpu_power_limit_watts": ("gauge", "Enforced GPU power limit."),
    "mlnode_gpu_sm_clock_hertz": ("gauge", "Current SM clock."),
    "mlnode_gpu_sm_clock_max_hertz": ("gauge", "Maximum SM clock."),
    "mlnode_gpu_clocks_event_reasons": ("gauge", "NVML clocks event/throttle reasons bitmask."),
    "mlnode_gpu_memory_used_bytes": ("gauge", "GPU memory used."),
    "mlnode_gpu_memory_free_bytes": ("gauge", "GPU memory free."),
    "mlnode_gpu_ecc_dbe_total": ("counter", "Aggregate ECC double-bit errors."),
    "mlnode_gpu_pcie_replay_total": ("counter", "PCIe replay counter."),
    "mlnode_gpu_xid_events_total": ("counter", "XID critical errors observed since process start."),
    "mlnode_host_cpu_busy_ratio": ("gauge", "Host CPU busy fraction since the previous scrape."),
    "mlnode_host_cpu_steal_ratio": ("gauge", "Host CPU steal fraction since the previous scrape."),
    "mlnode_host_memory_used_bytes": ("gauge", "Container memory usage from the cgroup."),
    "mlnode_host_memory_limit_bytes": ("gauge", "Container memory limit from the cgroup (absent when unlimited)."),
    "mlnode_host_hf_cache_free_bytes": ("gauge", "Free disk space of the HF cache volume."),
}


def _family_of(line: str) -> str:
    return line.split("{", 1)[0].split(" ", 1)[0]


_warned_unlisted: set = set()


def grouped_with_meta(lines) -> list:
    """Group exporter-owned sample lines by family, prefixing HELP/TYPE.

    The exposition format wants a family's samples contiguous under its TYPE
    header; sources emit per-GPU / per-source, so regrouping happens here.
    """
    families: dict = {}
    for line in lines:
        families.setdefault(_family_of(line), []).append(line)
    out = []
    for family in sorted(families):
        meta = FAMILY_META.get(family)
        if meta:
            kind, help_text = meta
            out.append(f"# HELP {family} {help_text}")
            out.append(f"# TYPE {family} {kind}")
        elif family not in _warned_unlisted:
            # a family missing here silently degrades to untyped downstream —
            # schema additions must come with a FAMILY_META entry
            logger.warning(f"metric family {family} has no FAMILY_META entry")
            _warned_unlisted.add(family)
        out.extend(families[family])
    return out
