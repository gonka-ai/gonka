#!/usr/bin/env python3
"""
Plot honest-vs-fraud Length vs Distance scatter for inference validation runs.

Scans benchmarks/data/inference_validation/ for experiment directories, loads
inference_validation_results.jsonl from each, computes distances using
validation.analysis.process_data, optionally finds optimal bounds, and produces
a combined scatter plot (like the qwen235B_thresholds notebook).

Honest vs fraud is determined automatically: if inference and validation models
match (from the config JSONs), the run is honest; otherwise it is fraud.

Usage:
    # Auto-detect all runs under data/inference_validation/:
    python scripts/analysis/inference_length_vs_distance.py

    # Specify explicit experiment dirs (--honest / --fraud):
    python scripts/analysis/inference_length_vs_distance.py \
        --honest data/inference_validation/exp_honest__2026-02-26_010603 \
        --fraud  data/inference_validation/exp_fraud__2026-02-26_020000

    # Limit number of items loaded per run:
    python scripts/analysis/inference_length_vs_distance.py --n-items 500

    # Compute optimal bounds (requires both honest and fraud data):
    python scripts/analysis/inference_length_vs_distance.py --find-bounds

    # Supply pre-computed bounds:
    python scripts/analysis/inference_length_vs_distance.py \
        --lower-bound 0.040733 --upper-bound 0.041733

    # Change output directory:
    python scripts/analysis/inference_length_vs_distance.py --out data/plots
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Dict, List, Optional, Tuple

BENCHMARKS_DIR = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(BENCHMARKS_DIR / "src"))
sys.path.insert(0, str(BENCHMARKS_DIR.parent / "common" / "src"))

import numpy as np
import matplotlib.pyplot as plt
from matplotlib.lines import Line2D

from validation.data import load_from_jsonl, ValidationItem
from validation.analysis import process_data, find_optimal_bounds_parallel

DATA_ROOT = BENCHMARKS_DIR / "data" / "inference_validation"
DEFAULT_PLOTS_DIR = BENCHMARKS_DIR / "data" / "plots"


def resolve_path(raw: str) -> Path:
    p = Path(raw)
    if p.is_absolute() and p.is_dir():
        return p
    for candidate in [Path.cwd() / p, BENCHMARKS_DIR / p]:
        if candidate.is_dir():
            return candidate.resolve()
    return p.resolve()


def _select_validation_artifact(exp_dir: Path) -> Optional[Path]:
    """Pick validation artifact from an experiment directory.

    Supports both default and tagged filenames:
    - inference_validation_results.jsonl
    - inference_validation_results__<tag>.jsonl
    """
    default_path = exp_dir / "inference_validation_results.jsonl"
    if default_path.exists():
        return default_path

    tagged = sorted(exp_dir.glob("inference_validation_results*.jsonl"))
    if not tagged:
        return None
    # Most recent tagged artifact is typically the one we want.
    tagged.sort(key=lambda p: p.stat().st_mtime, reverse=True)
    return tagged[0]


def _select_validation_config(exp_dir: Path) -> Optional[Path]:
    """Pick validation config from an experiment directory.

    Supports both default and tagged filenames:
    - validation_config.json
    - validation_config__<tag>.json
    """
    default_path = exp_dir / "validation_config.json"
    if default_path.exists():
        return default_path

    tagged = sorted(exp_dir.glob("validation_config*.json"))
    if not tagged:
        return None
    tagged.sort(key=lambda p: p.stat().st_mtime, reverse=True)
    return tagged[0]


def _is_honest(exp_dir: Path) -> Optional[bool]:
    """Determine honesty from validation_config + inference_config model names."""
    inf_cfg = exp_dir / "inference_config.json"
    val_cfg = _select_validation_config(exp_dir)
    if not inf_cfg.exists() or val_cfg is None:
        return None
    try:
        inf = json.loads(inf_cfg.read_text())
        val = json.loads(val_cfg.read_text())
        inf_model = inf.get("model_info", {}).get("name", "")
        val_model = val.get("validation_model_info", {}).get("name", "")
        if not inf_model or not val_model:
            return None
        return inf_model == val_model
    except Exception:
        return None


def _make_label(exp_dir: Path) -> str:
    return exp_dir.name


def discover_experiments() -> Tuple[Dict[str, Path], Dict[str, Path]]:
    honest: Dict[str, Path] = {}
    fraud: Dict[str, Path] = {}
    if not DATA_ROOT.exists():
        return honest, fraud
    for d in sorted(DATA_ROOT.iterdir()):
        if not d.is_dir():
            continue
        jsonl = _select_validation_artifact(d)
        if jsonl is None:
            continue
        h = _is_honest(d)
        label = _make_label(d)
        if h is True:
            honest[label] = d
        elif h is False:
            fraud[label] = d
        else:
            honest[label] = d
    return honest, fraud


def load_experiment(
    exp_dir: Path,
    n: Optional[int] = None,
) -> Tuple[List[ValidationItem], List[float], List[float]]:
    jsonl = _select_validation_artifact(exp_dir)
    if jsonl is None:
        raise RuntimeError(f"No validation artifact found in {exp_dir}")
    items = load_from_jsonl(str(jsonl), n=n)
    items, distances, topk = process_data(items)
    return items, distances, topk


def plot_length_vs_distance(
    title: str,
    honest_items_dict: Dict[str, List[ValidationItem]],
    honest_distances_dict: Dict[str, List[float]],
    fraud_items_dict: Dict[str, List[ValidationItem]],
    fraud_distances_dict: Dict[str, List[float]],
    bounds: Optional[Tuple[float, float]] = None,
    save_to: Optional[Path] = None,
) -> None:
    """Standalone version of the notebook's plot_length_vs_distance_comparison."""
    honest_keys = list(honest_items_dict.keys())
    fraud_keys = list(fraud_items_dict.keys())

    honest_lengths: Dict[str, List[int]] = {
        k: [len(item.inference_result.text) for item in honest_items_dict[k]]
        for k in honest_keys
    }
    fraud_lengths: Dict[str, List[int]] = {
        k: [len(item.inference_result.text) for item in fraud_items_dict[k]]
        for k in fraud_keys
    }

    fig, ax = plt.subplots(figsize=(12, 7))
    marker_size = 36

    honest_palette = ["#0B3D91", "#87CEFA", "#20B2AA"]
    fraud_palette_fn = lambda n: [plt.cm.Reds(v) for v in np.linspace(0.8, 0.4, max(1, n))]

    honest_colors = {k: honest_palette[i % len(honest_palette)] for i, k in enumerate(honest_keys)}
    fraud_colors = {k: fraud_palette_fn(len(fraud_keys))[i] for i, k in enumerate(fraud_keys)}

    fixed_marker_map = {"sp": "^", "en": "o", "ch": "s", "ar": "D", "hi": "P"}
    fallback_markers = ["v", "*", "X", "h", "<", ">", "1", "2", "3", "4"]

    def _norm_lang(item: ValidationItem) -> str:
        return str(getattr(item, "language", None) or "unk")

    all_langs_ordered: List[str] = []
    seen: set = set()
    for k in honest_keys:
        for item in honest_items_dict[k]:
            lang = _norm_lang(item)
            if lang not in seen:
                seen.add(lang)
                all_langs_ordered.append(lang)
    for k in fraud_keys:
        for item in fraud_items_dict[k]:
            lang = _norm_lang(item)
            if lang not in seen:
                seen.add(lang)
                all_langs_ordered.append(lang)

    marker_map = dict(fixed_marker_map)
    unknown_idx = 0
    for lang in all_langs_ordered:
        if lang not in marker_map:
            marker_map[lang] = fallback_markers[unknown_idx % len(fallback_markers)]
            unknown_idx += 1

    def _scatter_group(keys, items_dict, lengths_dict, distances_dict, color_map):
        for k in keys:
            langs = [_norm_lang(item) for item in items_dict[k]]
            xs = lengths_dict[k]
            ys = distances_dict[k]
            color = color_map[k]
            for lang in all_langs_ordered:
                idxs = [i for i, l in enumerate(langs) if l == lang]
                if not idxs:
                    continue
                ax.scatter(
                    [xs[i] for i in idxs],
                    [ys[i] for i in idxs],
                    c=color,
                    marker=marker_map.get(lang, "o"),
                    alpha=0.6,
                    s=marker_size,
                )

    _scatter_group(honest_keys, honest_items_dict, honest_lengths, honest_distances_dict, honest_colors)
    _scatter_group(fraud_keys, fraud_items_dict, fraud_lengths, fraud_distances_dict, fraud_colors)

    if bounds is not None:
        lower, upper = bounds
        ax.axhline(lower, color="blue", linestyle="--", linewidth=1.5)
        ax.axhline(upper, color="purple", linestyle="--", linewidth=1.5)

    group_handles = []
    for k in honest_keys:
        group_handles.append(
            Line2D([0], [0], marker="o", color=honest_colors[k], linestyle="None", markersize=8,
                   label=f"Honest - {k}")
        )
    for k in fraud_keys:
        group_handles.append(
            Line2D([0], [0], marker="o", color=fraud_colors[k], linestyle="None", markersize=8,
                   label=f"Fraud - {k}")
        )
    legend1 = ax.legend(handles=group_handles, title="Groups", loc="upper left", fontsize=8)
    ax.add_artist(legend1)

    language_name_map = {"sp": "Spanish", "en": "English", "ch": "Chinese", "ar": "Arabic", "hi": "Hindi", "unk": "Unknown"}
    lang_handles = [
        Line2D([0], [0], marker=marker_map[lang], color="black", linestyle="None", markersize=8,
               label=language_name_map.get(lang, lang))
        for lang in all_langs_ordered
        if lang in marker_map
    ]
    if lang_handles:
        legend2 = ax.legend(handles=lang_handles, title="Languages", loc="upper right", fontsize=8)
        ax.add_artist(legend2)

    if bounds is not None:
        lower, upper = bounds
        bounds_handles = [
            Line2D([0], [0], color="blue", linestyle="--", linewidth=2, label=f"Lower: {lower:.6f}"),
            Line2D([0], [0], color="purple", linestyle="--", linewidth=2, label=f"Upper: {upper:.6f}"),
        ]
        ax.legend(handles=bounds_handles, title="Bounds", loc="lower right", fontsize=8)

    ax.set_title(f"{title} - Length vs Distance Comparison", fontsize=14)
    ax.set_xlabel("Length (characters)", fontsize=12)
    ax.set_ylabel("Distance", fontsize=12)
    ax.grid(True, alpha=0.3)
    fig.tight_layout()

    if save_to is not None:
        save_to.mkdir(parents=True, exist_ok=True)
        safe = title.strip().replace(" ", "_").replace("/", "_").replace("\\", "_") or "comparison"
        filepath = save_to / f"{safe}_length_vs_distance.png"
        fig.savefig(filepath, dpi=300, bbox_inches="tight")
        print(f"Saved plot to: {filepath}")
    else:
        plt.show()

    plt.close(fig)


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Plot Length vs Distance for inference validation experiments"
    )
    parser.add_argument("--honest", type=str, nargs="*", default=None,
                        help="Paths to honest experiment directories")
    parser.add_argument("--fraud", type=str, nargs="*", default=None,
                        help="Paths to fraud experiment directories")
    parser.add_argument("--title", type=str, default=None,
                        help="Plot title (default: auto-generated)")
    parser.add_argument("--n-items", type=int, default=None,
                        help="Max items to load per experiment")
    parser.add_argument("--find-bounds", action="store_true",
                        help="Run F1-optimal bound search (requires honest + fraud)")
    parser.add_argument("--lower-bound", type=float, default=None,
                        help="Pre-computed lower bound")
    parser.add_argument("--upper-bound", type=float, default=None,
                        help="Pre-computed upper bound")
    parser.add_argument("--out", type=str, default=None,
                        help=f"Output directory for plots (default: {DEFAULT_PLOTS_DIR})")
    args = parser.parse_args()

    if args.honest is not None or args.fraud is not None:
        honest_dirs = {_make_label(resolve_path(p)): resolve_path(p) for p in (args.honest or [])}
        fraud_dirs = {_make_label(resolve_path(p)): resolve_path(p) for p in (args.fraud or [])}
    else:
        honest_dirs, fraud_dirs = discover_experiments()

    if not honest_dirs and not fraud_dirs:
        print(f"Error: no experiment directories found under {DATA_ROOT}")
        sys.exit(1)

    print(f"Honest experiments ({len(honest_dirs)}):")
    for name in honest_dirs:
        print(f"  - {name}")
    print(f"Fraud experiments ({len(fraud_dirs)}):")
    for name in fraud_dirs:
        print(f"  - {name}")
    print()

    honest_items_dict: Dict[str, List] = {}
    honest_distances_dict: Dict[str, List] = {}
    fraud_items_dict: Dict[str, List] = {}
    fraud_distances_dict: Dict[str, List] = {}

    all_honest_distances: List[float] = []
    all_fraud_distances: List[float] = []

    for name, d in honest_dirs.items():
        print(f"Loading honest: {name}")
        items, distances, _ = load_experiment(d, args.n_items)
        honest_items_dict[name] = items
        honest_distances_dict[name] = distances
        all_honest_distances.extend(distances)

    for name, d in fraud_dirs.items():
        print(f"Loading fraud: {name}")
        items, distances, _ = load_experiment(d, args.n_items)
        fraud_items_dict[name] = items
        fraud_distances_dict[name] = distances
        all_fraud_distances.extend(distances)

    bounds = None
    if args.lower_bound is not None and args.upper_bound is not None:
        bounds = (args.lower_bound, args.upper_bound)
        print(f"\nUsing pre-computed bounds: lower={bounds[0]:.6f}, upper={bounds[1]:.6f}")
    elif args.find_bounds:
        if not all_honest_distances or not all_fraud_distances:
            print("Error: --find-bounds requires both honest and fraud data")
            sys.exit(1)
        print("\nSearching for optimal bounds...")
        lower, upper = find_optimal_bounds_parallel(
            np.array(all_honest_distances), np.array(all_fraud_distances)
        )
        bounds = (lower, upper)

    title = args.title or "Inference Validation"
    plots_dir = Path(args.out) if args.out else DEFAULT_PLOTS_DIR

    plot_length_vs_distance(
        title=title,
        honest_items_dict=honest_items_dict,
        honest_distances_dict=honest_distances_dict,
        fraud_items_dict=fraud_items_dict,
        fraud_distances_dict=fraud_distances_dict,
        bounds=bounds,
        save_to=plots_dir,
    )

    if all_honest_distances:
        h = np.array(all_honest_distances)
        print(f"\nHonest distances: n={len(h)}, mean={h.mean():.6f}, "
              f"median={np.median(h):.6f}, max={h.max():.6f}")
    if all_fraud_distances:
        f = np.array(all_fraud_distances)
        print(f"Fraud distances:  n={len(f)}, mean={f.mean():.6f}, "
              f"median={np.median(f):.6f}, max={f.max():.6f}")

    print("\nDone.")


if __name__ == "__main__":
    main()
