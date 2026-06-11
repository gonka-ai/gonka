#!/usr/bin/env python3
"""
Estimate validation thresholds with repeated held-out evaluation.

Fits bounds on a train split (default 80%) of honest + fraud calibration
distances, evaluates fraud detection / false-positive rates on the held-out
test split, and repeats several times to report mean ± std.

Also reports in-sample bounds/metrics on the full calibration set for
comparison (the number typically computed in threshold notebooks).

Usage:
    python scripts/analysis/threshold_holdout_evaluation.py \\
        --honest data/2b-inference_results/validation_results_int8.jsonl \\
        --fraud  data/2b-inference_results/validation_results_int4.jsonl

    python scripts/analysis/threshold_holdout_evaluation.py \\
        --honest exp_a/validation_results.jsonl exp_b/validation_results.jsonl \\
        --fraud  exp_c/validation_results.jsonl \\
        --train-fraction 0.8 --n-repeats 10 --step 0.001 \\
        --out results/holdout_report.json
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Iterable, List, Sequence

BENCHMARKS_DIR = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(BENCHMARKS_DIR / "src"))
sys.path.insert(0, str(BENCHMARKS_DIR.parent / "common" / "src"))

import numpy as np

from validation.data import load_from_jsonl
from validation.analysis import held_out_threshold_evaluation, process_data


def resolve_path(raw: str) -> Path:
    p = Path(raw)
    if p.is_absolute() and p.exists():
        return p
    for candidate in [Path.cwd() / p, BENCHMARKS_DIR / p]:
        if candidate.exists():
            return candidate.resolve()
    return p.resolve()


def load_distances(paths: Sequence[str], n_items: int | None, topk: int | None) -> tuple[list, np.ndarray]:
    items: List = []
    for raw_path in paths:
        path = resolve_path(raw_path)
        if not path.exists():
            raise FileNotFoundError(f"Artifact not found: {path}")
        items.extend(load_from_jsonl(str(path), n=n_items))

    if not items:
        raise RuntimeError(f"No rows loaded from: {paths}")

    _, distances, _ = process_data(items, topk=topk)
    return items, np.asarray(distances, dtype=float)


def _fmt_rate(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:.4f}"


def _fmt_mean_std(summary: dict, key: str) -> str:
    stats = summary[key]
    return f"{stats['mean']:.6f} ± {stats['std']:.6f}"


def print_report(report: dict) -> None:
    params = report["parameters"]
    in_sample = report["in_sample"]
    summary = report["holdout_summary"]
    in_metrics = in_sample["metrics"]

    print("=== Calibration set ===")
    print(f"Honest samples: {params['n_honest']}")
    print(f"Fraud samples:  {params['n_fraud']}")
    print(f"Train fraction: {params['train_fraction']}")
    print(f"Repeats:        {params['n_repeats']}")
    print(f"Search step:    {params['step']}")
    print()

    print("=== In-sample (full calibration set) ===")
    print(f"Lower bound:           {in_sample['lower_bound']:.6f}")
    print(f"Upper bound:           {in_sample['upper_bound']:.6f}")
    print(f"Fraud detection rate:  {_fmt_rate(in_metrics['fraud_detection_rate'])}")
    print(f"Honest false pos rate: {_fmt_rate(in_metrics['honest_false_positive_rate'])}")
    print(f"F1:                    {_fmt_rate(in_metrics['f1'])}")
    print()

    print("=== Held-out (mean ± std over repeats) ===")
    print(f"Lower bound:           {_fmt_mean_std(summary, 'lower_bound')}")
    print(f"Upper bound:           {_fmt_mean_std(summary, 'upper_bound')}")
    print(
        "Fraud detection rate:  "
        f"{_fmt_mean_std(summary, 'test_metrics.fraud_detection_rate')}"
    )
    print(
        "Honest false pos rate: "
        f"{_fmt_mean_std(summary, 'test_metrics.honest_false_positive_rate')}"
    )
    print(f"F1 (test):             {_fmt_mean_std(summary, 'test_metrics.f1')}")
    print()

    print("=== Held-out train split (in-sample within each fold) ===")
    print(
        "Fraud detection rate:  "
        f"{_fmt_mean_std(summary, 'train_metrics.fraud_detection_rate')}"
    )
    print(
        "Honest false pos rate: "
        f"{_fmt_mean_std(summary, 'train_metrics.honest_false_positive_rate')}"
    )


def main(argv: Iterable[str] | None = None) -> None:
    parser = argparse.ArgumentParser(
        description=(
            "Repeated held-out threshold evaluation: fit on a train split, "
            "report out-of-sample fraud detection / false-positive rates."
        )
    )
    parser.add_argument(
        "--honest",
        nargs="+",
        required=True,
        help="One or more validation JSONL files with honest inference-validation rows.",
    )
    parser.add_argument(
        "--fraud",
        nargs="+",
        required=True,
        help="One or more validation JSONL files with fraudulent inference-validation rows.",
    )
    parser.add_argument(
        "--train-fraction",
        type=float,
        default=0.8,
        help="Fraction of each class used for fitting bounds (default: 0.8).",
    )
    parser.add_argument(
        "--n-repeats",
        type=int,
        default=5,
        help="Number of random train/test splits (default: 5).",
    )
    parser.add_argument(
        "--step",
        type=float,
        default=0.001,
        help="Grid step for lower-bound search (default: 0.001).",
    )
    parser.add_argument(
        "--seed",
        type=int,
        default=42,
        help="Random seed for held-out splits (default: 42).",
    )
    parser.add_argument(
        "--n-items",
        type=int,
        default=None,
        help="Optional cap on rows loaded from each JSONL file.",
    )
    parser.add_argument(
        "--topk",
        type=int,
        default=None,
        help="Optional top-k logprobs trim passed to process_data.",
    )
    parser.add_argument(
        "--n-jobs",
        type=int,
        default=-1,
        help="Parallel workers for bound search (default: all cores).",
    )
    parser.add_argument(
        "--out",
        type=Path,
        default=None,
        help="Optional path to write the full JSON report.",
    )
    parser.add_argument(
        "--verbose-search",
        action="store_true",
        help="Print progress from find_optimal_bounds_parallel.",
    )
    args = parser.parse_args(list(argv) if argv is not None else None)

    print("Loading honest artifacts...")
    _, honest_distances = load_distances(args.honest, args.n_items, args.topk)
    print(f"  loaded {len(honest_distances)} honest distances")

    print("Loading fraud artifacts...")
    _, fraud_distances = load_distances(args.fraud, args.n_items, args.topk)
    print(f"  loaded {len(fraud_distances)} fraud distances")
    print()

    report = held_out_threshold_evaluation(
        honest_distances,
        fraud_distances,
        train_fraction=args.train_fraction,
        n_repeats=args.n_repeats,
        step=args.step,
        n_jobs=args.n_jobs,
        random_seed=args.seed,
        verbose=args.verbose_search,
    )

    print_report(report)

    if args.out is not None:
        out_path = args.out if args.out.is_absolute() else resolve_path(str(args.out))
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        print(f"Wrote report -> {out_path}")


if __name__ == "__main__":
    main()
