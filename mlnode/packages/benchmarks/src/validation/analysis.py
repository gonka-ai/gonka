import numpy as np
from sklearn.metrics import f1_score
import matplotlib.pyplot as plt
from collections import Counter
from tqdm import tqdm
from joblib import Parallel, delayed
from collections.abc import Hashable, Mapping, Sequence
from typing import Any, Dict, List, Optional, Tuple, Union

from validation.utils import distance2
from validation import stats


def _truncate_position_logprobs(result, topk: Optional[int]):
    """Return a lightweight copy of Result with per-position logprobs trimmed to topk."""
    if topk is None:
        return result

    cloned_positions = []
    for pos in result.results:
        trimmed = dict(list(pos.logprobs.items())[:topk])
        cloned_positions.append(pos.model_copy(update={"logprobs": trimmed}))
    return result.model_copy(update={"results": cloned_positions})


def process_data(items, topk: Optional[int] = None):
    if topk is not None and topk <= 0:
        raise ValueError(f"topk must be positive or None, got {topk}")

    distances = [
        distance2(
            _truncate_position_logprobs(item.inference_result, topk),
            _truncate_position_logprobs(item.validation_result, topk),
        )
        for item in items
    ]


    top_k_matches_ratios = [d[1] for d in distances]
    distances = [d[0] for d in distances]


    def clean_data(items, distances, top_k_matches_ratios):
        """
        Fix case when tokens sequences don't match
        """
        original_len = len(items)
        drop_items = []
        for item, d in zip(items, distances):
            if d == -1:
                drop_items.append(item)
            
        items = [item for item in items if item not in drop_items]
        distances = [distance for distance in distances if distance != -1]
        top_k_matches_ratios = [ratio for ratio in top_k_matches_ratios if ratio != -1]
        print(f"Dropped {len(drop_items)} / {original_len} items")

        return items, distances, top_k_matches_ratios


    items, distances, top_k_matches_ratios = clean_data(items, distances, top_k_matches_ratios)
    return items, distances, top_k_matches_ratios



def analyze(distances, top_k_matches_ratios):
    stats.describe_data(distances, name="distances")
    stats.describe_data(top_k_matches_ratios, name="top_k_matches_ratios")
    best_fit, fit_results = stats.select_best_fit(distances)
    stats.plot_real_vs_fitted(distances, dist_name=best_fit.dist_name, bins=250)

    return best_fit, fit_results


def plot_distances_and_matches(items, distances, top_k_matches_ratios, title_prefix=""):
    """
    Plots two scatter plots side by side:
      1) Distances vs. # of tokens
      2) Top-K Matches Ratios vs. # of tokens
    """
    n_tokens = [len(item.inference_result.results) for item in items]
    
    if len(title_prefix) > 40:
        formatted_prefix = title_prefix.replace('/', '/\n').replace('___', '___\n')
    else:
        formatted_prefix = title_prefix
    
    plt.figure(figsize=(12, 5))
    
    plt.subplot(1, 2, 1)
    plt.scatter(n_tokens, distances, alpha=0.3)
    plt.xlabel("Number of tokens")
    plt.ylabel("Distance")
    plt.title(f"{formatted_prefix}\nDistance vs. #tokens")

    plt.subplot(1, 2, 2)
    plt.scatter(n_tokens, top_k_matches_ratios, alpha=0.3, color="orange")
    plt.xlabel("Number of tokens")
    plt.ylabel("Top-K Matches Ratio")
    plt.title(f"{formatted_prefix}\nTop-K Matches Ratio vs. #tokens")
    
    plt.tight_layout()
    plt.show()
    
    
def classify_data(distances, lower_bound, upper_bound):
    classifications = []
    for d in distances:
        if d < lower_bound:
            classifications.append('accepted')
        else:
            classifications.append('fraud')
    return classifications


def evaluate_bound(lower, upper_candidates, distances_val, distances_quant):
    if np.any(distances_val > lower):
        return None

    all_distances = np.concatenate([distances_val, distances_quant])
    labels_true = np.array([0] * len(distances_val) + [1] * len(distances_quant))

    best_f1 = -1
    optimal_upper = None
    for upper in upper_candidates:
        labels_pred = np.where(all_distances < lower, 0, 1)
        labels_pred[(all_distances >= lower) & (all_distances <= upper)] = 1
        current_f1 = f1_score(labels_true, labels_pred)
        if current_f1 > best_f1:
            best_f1 = current_f1
            optimal_upper = upper
    return lower, optimal_upper, best_f1


def compute_threshold_metrics(
    distances_honest: Union[Sequence[float], np.ndarray],
    distances_fraud: Union[Sequence[float], np.ndarray],
    lower_bound: float,
) -> Dict[str, Any]:
    """Classification metrics for a fixed lower bound (production rule: distance < lower => honest)."""
    distances_honest = np.asarray(distances_honest, dtype=float)
    distances_fraud = np.asarray(distances_fraud, dtype=float)

    honest_classes = classify_data(distances_honest, lower_bound, lower_bound)
    fraud_classes = classify_data(distances_fraud, lower_bound, lower_bound)

    n_honest = int(len(distances_honest))
    n_fraud = int(len(distances_fraud))
    n_honest_flagged = sum(c == "fraud" for c in honest_classes)
    n_fraud_detected = sum(c == "fraud" for c in fraud_classes)

    metrics: Dict[str, Any] = {
        "n_honest": n_honest,
        "n_fraud": n_fraud,
        "honest_accept_rate": None,
        "honest_false_positive_rate": None,
        "fraud_detection_rate": None,
        "f1": None,
    }
    if n_honest:
        metrics["honest_accept_rate"] = float((n_honest - n_honest_flagged) / n_honest)
        metrics["honest_false_positive_rate"] = float(n_honest_flagged / n_honest)
    if n_fraud:
        metrics["fraud_detection_rate"] = float(n_fraud_detected / n_fraud)

    if n_honest or n_fraud:
        all_distances = np.concatenate([distances_honest, distances_fraud])
        labels_true = np.array([0] * n_honest + [1] * n_fraud)
        labels_pred = np.where(all_distances < lower_bound, 0, 1)
        metrics["f1"] = float(f1_score(labels_true, labels_pred))

    return metrics


def _random_train_test_split(
    values: np.ndarray,
    train_fraction: float,
    rng: np.random.Generator,
) -> Tuple[np.ndarray, np.ndarray]:
    n = len(values)
    if n == 0:
        return values, values
    if n == 1:
        return values, values[:0]

    indices = rng.permutation(n)
    n_train = int(round(n * train_fraction))
    n_train = max(1, min(n_train, n - 1))
    train_idx = indices[:n_train]
    test_idx = indices[n_train:]
    return values[train_idx], values[test_idx]


def _mean_std(values: Sequence[float]) -> Dict[str, float]:
    arr = np.asarray(values, dtype=float)
    if arr.size == 0:
        return {"mean": float("nan"), "std": float("nan")}
    if arr.size == 1:
        return {"mean": float(arr[0]), "std": 0.0}
    return {"mean": float(arr.mean()), "std": float(arr.std(ddof=1))}


def held_out_threshold_evaluation(
    distances_honest: Union[Sequence[float], np.ndarray],
    distances_fraud: Union[Sequence[float], np.ndarray],
    *,
    train_fraction: float = 0.8,
    n_repeats: int = 5,
    step: float = 0.0001,
    n_jobs: int = -1,
    random_seed: int = 42,
    verbose: bool = False,
) -> Dict[str, Any]:
    """Repeated train/test splits to estimate out-of-sample threshold performance.

    Fits bounds on ``train_fraction`` of honest and fraud distances, evaluates
    classification metrics on the held-out remainder. Also reports in-sample
    bounds/metrics on the full calibration set for comparison.
    """
    if not 0.0 < train_fraction < 1.0:
        raise ValueError(f"train_fraction must be in (0, 1), got {train_fraction}")
    if n_repeats < 1:
        raise ValueError(f"n_repeats must be >= 1, got {n_repeats}")

    distances_honest = np.asarray(distances_honest, dtype=float)
    distances_fraud = np.asarray(distances_fraud, dtype=float)
    if len(distances_honest) == 0 or len(distances_fraud) == 0:
        raise ValueError("Both honest and fraud distance arrays must be non-empty")

    rng = np.random.default_rng(random_seed)
    folds: List[Dict[str, Any]] = []

    for repeat in range(n_repeats):
        split_rng = np.random.default_rng(rng.integers(0, 2**32 - 1))
        train_honest, test_honest = _random_train_test_split(distances_honest, train_fraction, split_rng)
        train_fraud, test_fraud = _random_train_test_split(distances_fraud, train_fraction, split_rng)

        lower, upper = find_optimal_bounds_parallel(
            train_honest,
            train_fraud,
            step=step,
            n_jobs=n_jobs,
            verbose=verbose,
        )
        folds.append(
            {
                "repeat": repeat,
                "n_train_honest": int(len(train_honest)),
                "n_test_honest": int(len(test_honest)),
                "n_train_fraud": int(len(train_fraud)),
                "n_test_fraud": int(len(test_fraud)),
                "lower_bound": float(lower),
                "upper_bound": float(upper),
                "train_metrics": compute_threshold_metrics(train_honest, train_fraud, lower),
                "test_metrics": compute_threshold_metrics(test_honest, test_fraud, lower),
            }
        )

    metric_keys = [
        "lower_bound",
        "upper_bound",
        "test_metrics.honest_false_positive_rate",
        "test_metrics.honest_accept_rate",
        "test_metrics.fraud_detection_rate",
        "test_metrics.f1",
        "train_metrics.honest_false_positive_rate",
        "train_metrics.fraud_detection_rate",
        "train_metrics.f1",
    ]

    def _fold_value(fold: Dict[str, Any], key: str) -> float:
        if key in {"lower_bound", "upper_bound"}:
            return float(fold[key])
        section, field = key.split(".", 1)
        value = fold[section][field]
        if value is None:
            return float("nan")
        return float(value)

    holdout_summary: Dict[str, Dict[str, float]] = {}
    for key in metric_keys:
        holdout_summary[key] = _mean_std([_fold_value(fold, key) for fold in folds])

    in_sample_lower, in_sample_upper = find_optimal_bounds_parallel(
        distances_honest,
        distances_fraud,
        step=step,
        n_jobs=n_jobs,
        verbose=verbose,
    )

    return {
        "parameters": {
            "train_fraction": train_fraction,
            "n_repeats": n_repeats,
            "step": step,
            "random_seed": random_seed,
            "n_honest": int(len(distances_honest)),
            "n_fraud": int(len(distances_fraud)),
        },
        "in_sample": {
            "lower_bound": float(in_sample_lower),
            "upper_bound": float(in_sample_upper),
            "metrics": compute_threshold_metrics(distances_honest, distances_fraud, in_sample_lower),
        },
        "holdout_folds": folds,
        "holdout_summary": holdout_summary,
    }


def find_optimal_bounds_parallel(
    distances_val,
    distances_quant,
    step=0.0001,
    n_jobs=-1,
    verbose: bool = True,
):
    all_distances = np.concatenate([distances_val, distances_quant])
    min_dist, max_dist = all_distances.min(), all_distances.max()
    search_space = np.arange(min_dist, max_dist, step)

    results = Parallel(n_jobs=n_jobs)(
        delayed(evaluate_bound)(
            lower,
            search_space[search_space > lower],
            distances_val,
            distances_quant
        )
        for lower in tqdm(search_space, desc="Searching optimal bounds", disable=not verbose)
    )

    # Remove None results that violate the constraint
    results = [r for r in results if r is not None]

    if not results:
        raise ValueError("No valid bounds found under the constraint that no distances_val exceed the lower bound.")

    optimal_lower, optimal_upper, best_f1 = max(results, key=lambda x: x[2])

    if verbose:
        print(f"Optimal Lower Bound: {optimal_lower:.6f}")
        print(f"Best F1-Score: {best_f1:.4f}")

    return optimal_lower, optimal_upper


def plot_classification_results(
    distances,
    classifications,
    lower_bound,
    upper_bound,
    title_prefix="",
    point_hue: Sequence[Hashable] | None = None,
):
    """
    If ``point_hue`` is None (default), scatter colors follow classification only (legacy).

    If ``point_hue`` is set, it must have the same length as ``distances``; each unique
    label gets a distinct color (tab20) and classification is shown via marker shape.
    """
    classification_counts = Counter(classifications)

    plt.figure(figsize=(14, 6))

    plt.subplot(1, 2, 1)
    plt.bar(classification_counts.keys(), classification_counts.values(), color=['green', 'orange', 'red'])
    plt.title(f"{title_prefix}\nClassification Counts")
    plt.xlabel("Classification")
    plt.ylabel("Count")

    plt.subplot(1, 2, 2)
    color_map = {'accepted': 'green', 'questionable': 'orange', 'fraud': 'red'}
    class_markers = {'accepted': 'o', 'questionable': 's', 'fraud': 'X'}

    if point_hue is None:
        for classification in classification_counts:
            idxs = [i for i, c in enumerate(classifications) if c == classification]
            plt.scatter(
                idxs, [distances[i] for i in idxs],
                c=color_map.get(classification, 'gray'), alpha=0.5,
                label=f"{classification.capitalize()} ({classification_counts[classification]})"
            )
    else:
        if len(point_hue) != len(distances):
            raise ValueError(
                f"point_hue length ({len(point_hue)}) must match distances ({len(distances)})"
            )
        unique_hues = list(dict.fromkeys(point_hue))
        cmap = plt.get_cmap("tab20")
        hue_colors = {h: cmap(i % 20) for i, h in enumerate(unique_hues)}
        for classification in classification_counts:
            marker = class_markers.get(classification, "o")
            for hue in unique_hues:
                idxs = [
                    i
                    for i, c in enumerate(classifications)
                    if c == classification and point_hue[i] == hue
                ]
                if not idxs:
                    continue
                plt.scatter(
                    idxs,
                    [distances[i] for i in idxs],
                    c=[hue_colors[hue]],
                    marker=marker,
                    alpha=0.5,
                    label=f"{classification} | {hue} ({len(idxs)})",
                )

    plt.axhline(lower_bound, color='blue', linestyle='--', label='Bound')
    plt.title(f"{title_prefix}\nDistances Classification")
    plt.xlabel("Item Index")
    plt.ylabel("Distance")
    _leg_kw = {"fontsize": 7, "loc": "best"} if point_hue is not None else {"loc": "best"}
    plt.legend(**_leg_kw)

    plt.tight_layout()
    plt.show()


def _get_item_text_length(item) -> int:
    """Extract len(inference_result.text) from either pydantic-like objects or dict rows."""
    try:
        if hasattr(item, "inference_result") and hasattr(item.inference_result, "text"):
            return len(item.inference_result.text)
    except Exception:
        pass
    if isinstance(item, dict):
        inf_res = item.get("inference_result")
        if isinstance(inf_res, dict) and "text" in inf_res:
            return len(inf_res["text"])
        if hasattr(inf_res, "text"):
            return len(inf_res.text)
    raise TypeError(f"Unsupported item type for text length: {type(item)}")


def plot_length_vs_distance_comparison(
    name,
    honest_items,
    honest_distances,
    fraud_items,
    fraud_distances,
    bounds=None,
    save_to=None,
    hue_by_series: bool = False,
):
    """Create combined length vs distance plot for comparison.

    If ``hue_by_series`` is False (default), honest points are blue and fraud points are red.

    If ``hue_by_series`` is True, each top-level series (dict key) gets its own color (tab20);
    honest series use marker ``o``, fraud series use marker ``^``. Only supported when
    ``honest_items`` / ``honest_distances`` (and fraud side) are dicts aligned by key.
    """
    def _flatten_items_and_distances(items, distances):
        if isinstance(items, Mapping) and isinstance(distances, Mapping):
            flat_items = []
            flat_distances = []
            for k in distances.keys():
                if k not in items:
                    continue
                subitems = items[k]
                subdistances = distances[k]
                if isinstance(subitems, Mapping):
                    raise TypeError(
                        "plot_length_vs_distance_comparison: expected items[k] to be a list, got Mapping"
                    )
                try:
                    subitems_list = list(subitems)
                except TypeError as e:
                    raise TypeError(
                        f"plot_length_vs_distance_comparison: items[{k!r}] is not iterable: {type(subitems)}"
                    ) from e
                try:
                    subdist_list = list(subdistances)
                except TypeError as e:
                    raise TypeError(
                        f"plot_length_vs_distance_comparison: distances[{k!r}] is not iterable: {type(subdistances)}"
                    ) from e
                for item, d in zip(subitems_list, subdist_list):
                    flat_items.append(item)
                    flat_distances.append(d)
            return flat_items, flat_distances

        if isinstance(distances, Mapping):
            raise TypeError(
                "plot_length_vs_distance_comparison: unsupported combination of items/distances types: "
                f"{type(items)} vs {type(distances)}"
            )

        items_seq = list(items.values()) if isinstance(items, Mapping) else list(items)
        dist_seq = list(distances)
        return items_seq, dist_seq

    plt.figure(figsize=(10, 6))

    if hue_by_series:
        if not isinstance(honest_items, Mapping) or not isinstance(fraud_items, Mapping):
            raise TypeError(
                "hue_by_series=True requires honest_items and fraud_items to be dicts."
            )
        if not isinstance(honest_distances, Mapping) or not isinstance(fraud_distances, Mapping):
            raise TypeError(
                "hue_by_series=True requires honest_distances and fraud_distances to be dicts "
                "(same keys as the corresponding items dicts)."
            )
        series_keys = list(dict.fromkeys(list(honest_distances.keys()) + list(fraud_distances.keys())))
        cmap = plt.get_cmap("tab20")
        series_colors = {k: cmap(i % 20) for i, k in enumerate(series_keys)}

        for k in honest_distances.keys():
            if k not in honest_items:
                continue
            subitems = honest_items[k]
            subdist = honest_distances[k]
            if isinstance(subitems, Mapping):
                raise TypeError(
                    "hue_by_series=True: expected honest_items[k] to be a list, got Mapping"
                )
            lengths = [_get_item_text_length(item) for item in subitems]
            plt.scatter(
                lengths,
                list(subdist),
                alpha=0.5,
                color=series_colors[k],
                marker="o",
                label=f"honest: {k}",
                s=10,
            )

        for k in fraud_distances.keys():
            if k not in fraud_items:
                continue
            subitems = fraud_items[k]
            subdist = fraud_distances[k]
            if isinstance(subitems, Mapping):
                raise TypeError(
                    "hue_by_series=True: expected fraud_items[k] to be a list, got Mapping"
                )
            lengths = [_get_item_text_length(item) for item in subitems]
            plt.scatter(
                lengths,
                list(subdist),
                alpha=0.5,
                color=series_colors[k],
                marker="^",
                label=f"fraud: {k}",
                s=10,
            )
    else:
        honest_flat = _flatten_items_and_distances(honest_items, honest_distances)
        honest_items_seq, honest_dist_vals = honest_flat
        honest_lengths = [_get_item_text_length(item) for item in honest_items_seq]

        fraud_flat = _flatten_items_and_distances(fraud_items, fraud_distances)
        fraud_items_seq, fraud_dist_vals = fraud_flat
        fraud_lengths = [_get_item_text_length(item) for item in fraud_items_seq]

        plt.scatter(honest_lengths, honest_dist_vals, alpha=0.5, color='blue', label='Honest Items', s=10)
        plt.scatter(fraud_lengths, fraud_dist_vals, alpha=0.5, color='red', label='Fraud Items', s=10)

    plt.title(f'{name} - Length vs Distance Comparison')
    plt.xlabel('Length (characters)')
    plt.ylabel('Distance')
    if hue_by_series:
        plt.legend(fontsize=7)
    else:
        plt.legend()
    plt.grid(True, alpha=0.3)

    if bounds is not None:
        try:
            lower, upper = bounds
            if lower is not None:
                plt.axhline(lower, color="blue", linestyle="--", linewidth=1, label="Lower bound")
            if upper is not None:
                plt.axhline(upper, color="orange", linestyle="--", linewidth=1, label="Upper bound")
        except Exception:
            pass

    if save_to:
        try:
            plt.savefig(save_to, bbox_inches="tight")
        except Exception:
            pass
    plt.show()
