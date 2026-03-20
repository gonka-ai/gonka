#!/usr/bin/env python3
"""
Compute pairwise L2 distances between PoC vectors from two runs and plot histograms.

Scans benchmarks/data/poc_calidation/ for run directories, pairs them up by
server name, matches nonces, and plots the L2 distance distribution with a
configurable percentile threshold line (like the reference "Honest distribution
+ threshold" plot).

Usage:
    # Auto-detect the two most recent runs and compare all common servers:
    python scripts/analysis/poc_l2_histogram.py

    # Compare two specific run directories:
    python scripts/analysis/poc_l2_histogram.py \
        --run-a data/poc_calidation/poc_validation_2026-02-25_205141 \
        --run-b data/poc_calidation/poc_validation_2026-02-25_205330

    # Change threshold percentile (default p98):
    python scripts/analysis/poc_l2_histogram.py --percentile 95

    # Specify output directory:
    python scripts/analysis/poc_l2_histogram.py --out data/plots
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Dict, List, Optional, Tuple

import matplotlib.pyplot as plt
import numpy as np

BENCHMARKS_DIR = Path(__file__).resolve().parents[2]
DATA_ROOT = BENCHMARKS_DIR / "data" / "poc_calidation"
DEFAULT_PLOTS_DIR = BENCHMARKS_DIR / "data" / "plots"


def load_run(run_dir: Path) -> Dict[str, Dict]:
    """Load all artifact files from a run directory.

    Returns {server_name: {"nonces": [...], "vectors": [...], ...}}
    """
    results: Dict[str, Dict] = {}
    for fpath in sorted(run_dir.glob("*.json")):
        if fpath.name in ("run_config.json", "config.json"):
            continue
        try:
            data = json.loads(fpath.read_text(encoding="utf-8"))
        except Exception:
            continue
        if "error" in data or "vectors" not in data:
            continue
        name = data.get("server_name", fpath.stem)
        results[name] = data
    return results


def match_vectors(
    data_a: Dict, data_b: Dict
) -> Tuple[np.ndarray, np.ndarray, List[int]]:
    """Find common nonces and return aligned vector arrays."""
    nonce_to_vec_a = {n: v for n, v in zip(data_a["nonces"], data_a["vectors"]) if v is not None}
    nonce_to_vec_b = {n: v for n, v in zip(data_b["nonces"], data_b["vectors"]) if v is not None}

    common = sorted(set(nonce_to_vec_a) & set(nonce_to_vec_b))
    if not common:
        return np.array([]), np.array([]), []

    vecs_a = np.array([nonce_to_vec_a[n] for n in common], dtype=np.float32)
    vecs_b = np.array([nonce_to_vec_b[n] for n in common], dtype=np.float32)
    return vecs_a, vecs_b, common


def compute_l2(vecs_a: np.ndarray, vecs_b: np.ndarray) -> np.ndarray:
    return np.linalg.norm(vecs_a - vecs_b, axis=1)


def find_recent_runs(n: int = 2) -> List[Path]:
    """Return the *n* most recent run directories under DATA_ROOT."""
    if not DATA_ROOT.exists():
        return []
    dirs = sorted(
        [d for d in DATA_ROOT.iterdir() if d.is_dir()],
        key=lambda d: d.name,
        reverse=True,
    )
    return dirs[:n]


def resolve_run_path(raw: str) -> Path:
    """Resolve a run path that may be relative to CWD or BENCHMARKS_DIR."""
    p = Path(raw)
    if p.is_absolute() and p.is_dir():
        return p
    candidates = [Path.cwd() / p, BENCHMARKS_DIR / p]
    for c in candidates:
        if c.is_dir():
            return c.resolve()
    return p.resolve()


def plot_histogram(
    distances: np.ndarray,
    percentile: float,
    server_name: str,
    run_a_name: str,
    run_b_name: str,
    out_path: Path,
) -> None:
    threshold = float(np.percentile(distances, percentile))

    fig, ax = plt.subplots(figsize=(10, 6))
    ax.hist(distances, bins=60, color="#5cb85c", edgecolor="#4a9a4a", alpha=0.9,
            label=f"Honest pairs (same-marker)")
    ax.axvline(threshold, color="red", linestyle="--", linewidth=2,
               label=f"p{int(percentile)}: {threshold:.4f}")
    ax.set_xlabel("L2 Distance", fontsize=13)
    ax.set_ylabel("Count", fontsize=13)
    ax.set_title("Honest distribution + threshold", fontsize=14)
    ax.legend(fontsize=12)
    ax.tick_params(labelsize=11)
    fig.tight_layout()
    fig.savefig(out_path, dpi=150)
    plt.close(fig)

    print(f"  [{server_name}] {len(distances)} pairs | "
          f"mean={distances.mean():.4f}  median={np.median(distances):.4f}  "
          f"p{int(percentile)}={threshold:.4f}  max={distances.max():.4f}")
    print(f"  -> {out_path}")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Plot L2 distance histograms between two PoC validation runs"
    )
    parser.add_argument("--run-a", type=str, default=None,
                        help="Path to first run directory")
    parser.add_argument("--run-b", type=str, default=None,
                        help="Path to second run directory")
    parser.add_argument("--percentile", type=float, default=98,
                        help="Threshold percentile for the dashed line (default: 98)")
    parser.add_argument("--out", type=str, default=None,
                        help=f"Output directory for plots (default: {DEFAULT_PLOTS_DIR})")
    parser.add_argument("--server", type=str, default=None,
                        help="Only plot for this server name (default: all common servers)")
    args = parser.parse_args()

    if args.run_a and args.run_b:
        run_a = resolve_run_path(args.run_a)
        run_b = resolve_run_path(args.run_b)
    elif args.run_a or args.run_b:
        parser.error("Specify both --run-a and --run-b, or neither (auto-detect)")
    else:
        recent = find_recent_runs(2)
        if len(recent) < 2:
            print(f"Error: need at least 2 run directories under {DATA_ROOT}")
            sys.exit(1)
        run_a, run_b = recent[1], recent[0]

    for label, d in [("run-a", run_a), ("run-b", run_b)]:
        if not d.is_dir():
            print(f"Error: {label} directory not found: {d}")
            sys.exit(1)

    plots_dir = Path(args.out) if args.out else DEFAULT_PLOTS_DIR
    plots_dir.mkdir(parents=True, exist_ok=True)

    print(f"Run A: {run_a.name}")
    print(f"Run B: {run_b.name}")
    print(f"Percentile: p{int(args.percentile)}")
    print(f"Output: {plots_dir}\n")

    data_a = load_run(run_a)
    data_b = load_run(run_b)

    if not data_a:
        print(f"Error: no valid artifact files in run A ({run_a})")
        sys.exit(1)
    if not data_b:
        print(f"Error: no valid artifact files in run B ({run_b})")
        sys.exit(1)

    common_servers = sorted(set(data_a) & set(data_b))
    if args.server:
        if args.server not in common_servers:
            print(f"Error: server '{args.server}' not found in both runs. "
                  f"Common servers: {common_servers}")
            sys.exit(1)
        common_servers = [args.server]

    if common_servers:
        print(f"Comparing servers: {common_servers}\n")
        for server in common_servers:
            vecs_a, vecs_b, nonces = match_vectors(data_a[server], data_b[server])
            if len(nonces) == 0:
                print(f"  [{server}] no common nonces, skipping")
                continue

            dists = compute_l2(vecs_a, vecs_b)
            fname = f"l2_hist_{server}_{run_a.name}_vs_{run_b.name}.png"
            plot_histogram(dists, args.percentile, server,
                           run_a.name, run_b.name, plots_dir / fname)
    else:
        servers_a = list(data_a.keys())
        servers_b = list(data_b.keys())
        print(f"No common server names (A={servers_a}, B={servers_b}), "
              f"cross-comparing first server from each run.\n")
        sa, sb = servers_a[0], servers_b[0]
        vecs_a, vecs_b, nonces = match_vectors(data_a[sa], data_b[sb])
        if len(nonces) == 0:
            print(f"  [{sa} vs {sb}] no common nonces")
            sys.exit(1)

        dists = compute_l2(vecs_a, vecs_b)
        label = f"{sa}_vs_{sb}"
        fname = f"l2_hist_{label}_{run_a.name}_vs_{run_b.name}.png"
        plot_histogram(dists, args.percentile, label,
                       run_a.name, run_b.name, plots_dir / fname)

    print("\nDone.")


if __name__ == "__main__":
    main()
