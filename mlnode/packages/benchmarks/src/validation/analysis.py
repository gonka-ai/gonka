import numpy as np
from sklearn.metrics import f1_score
import matplotlib.pyplot as plt
from collections import Counter
from tqdm import tqdm
from joblib import Parallel, delayed
from collections.abc import Hashable, Mapping, Sequence

from validation.utils import distance2
from validation import stats


def process_data(items):
    distances = [
        distance2(
            item.inference_result,
            item.validation_result,
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


def find_optimal_bounds_parallel(distances_val, distances_quant, step=0.0001, n_jobs=-1):
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
        for lower in tqdm(search_space, desc="Searching optimal bounds")
    )

    # Remove None results that violate the constraint
    results = [r for r in results if r is not None]

    if not results:
        raise ValueError("No valid bounds found under the constraint that no distances_val exceed the lower bound.")

    optimal_lower, optimal_upper, best_f1 = max(results, key=lambda x: x[2])

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
