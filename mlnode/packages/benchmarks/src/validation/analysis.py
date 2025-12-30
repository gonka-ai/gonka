import numpy as np
from sklearn.metrics import f1_score, recall_score
import matplotlib.pyplot as plt
from matplotlib.lines import Line2D
from collections import Counter
from tqdm import tqdm
from joblib import Parallel, delayed
import json
import os

from validation.utils import distance2
from validation import stats


def process_data(items):
    distances = [
        distance2(item.inference_result, item.validation_result)
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
    
    # Format title_prefix for better readability by breaking long paths
    if len(title_prefix) > 40:
        # Break on path separators and long underscores for better readability
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


def plot_classification_results(distances, classifications, lower_bound, upper_bound, title_prefix=""):
    classification_counts = Counter(classifications)

    plt.figure(figsize=(14, 6))

    plt.subplot(1, 2, 1)
    plt.bar(classification_counts.keys(), classification_counts.values(), color=['green', 'orange', 'red'])
    plt.title(f"{title_prefix}\nClassification Counts")
    plt.xlabel("Classification")
    plt.ylabel("Count")

    plt.subplot(1, 2, 2)
    color_map = {'accepted': 'green', 'questionable': 'orange', 'fraud': 'red'}
    for classification in classification_counts:
        idxs = [i for i, c in enumerate(classifications) if c == classification]
        plt.scatter(
            idxs, [distances[i] for i in idxs],
            c=color_map[classification], alpha=0.5,
            label=f"{classification.capitalize()} ({classification_counts[classification]})"
        )

    plt.axhline(lower_bound, color='blue', linestyle='--', label='Bound')
    plt.title(f"{title_prefix}\nDistances Classification")
    plt.xlabel("Item Index")
    plt.ylabel("Distance")
    plt.legend()

    plt.tight_layout()
    plt.show()


def plot_length_vs_distance_comparison(name, honest_items_dict, honest_distances_dict, fraud_items_dict, fraud_distances_dict, bounds=None, save_to=None):
    honest_keys = list(honest_items_dict.keys())
    fraud_keys = list(fraud_items_dict.keys())

    if set(honest_keys) != set(honest_distances_dict.keys()):
        raise ValueError("honest_items_dict and honest_distances_dict must have the same keys")
    if set(fraud_keys) != set(fraud_distances_dict.keys()):
        raise ValueError("fraud_items_dict and fraud_distances_dict must have the same keys")

    for k in honest_keys:
        if len(honest_items_dict[k]) != len(honest_distances_dict[k]):
            raise ValueError(f"Honest group '{k}' items and distances lengths differ")
    for k in fraud_keys:
        if len(fraud_items_dict[k]) != len(fraud_distances_dict[k]):
            raise ValueError(f"Fraud group '{k}' items and distances lengths differ")

    honest_lengths_dict = {k: [len(item.inference_result.text) for item in honest_items_dict[k]] for k in honest_keys}
    fraud_lengths_dict = {k: [len(item.inference_result.text) for item in fraud_items_dict[k]] for k in fraud_keys}

    plt.figure(figsize=(10, 6))

    marker_size = 36

    honest_palette = ['#0B3D91', '#87CEFA', '#20B2AA']
    honest_group_colors_seq = [honest_palette[i % len(honest_palette)] for i in range(max(1, len(honest_keys)))]
    fraud_group_colors_seq = [plt.cm.Reds(v) for v in np.linspace(0.8, 0.4, max(1, len(fraud_keys)))]
    honest_color_by_key = {k: honest_group_colors_seq[i] for i, k in enumerate(honest_keys)}
    fraud_color_by_key = {k: fraud_group_colors_seq[i] for i, k in enumerate(fraud_keys)}

    def _norm_lang(lang):
        return str(lang) if lang is not None else 'unk'

    languages_groups = {"honest": {}, "fraud": {}}
    for k in honest_keys:
        languages_groups["honest"][k] = [_norm_lang(getattr(item, 'language', None)) for item in honest_items_dict[k]]
    for k in fraud_keys:
        languages_groups["fraud"][k] = [_norm_lang(getattr(item, 'language', None)) for item in fraud_items_dict[k]]
    seen_langs = set()
    unique_languages = []
    for k in honest_keys:
        for lang in languages_groups["honest"][k]:
            if lang not in seen_langs:
                seen_langs.add(lang)
                unique_languages.append(lang)
    for k in fraud_keys:
        for lang in languages_groups["fraud"][k]:
            if lang not in seen_langs:
                seen_langs.add(lang)
                unique_languages.append(lang)

    fixed_marker_map = {
        'sp': '^',
        'en': 'o',
        'ch': 's',
        'ar': 'D',
        'hi': 'P',
    }
    fallback_markers = ['v', '*', 'X', 'h', '<', '>', '1', '2', '3', '4']
    unknown_langs = sorted([lang for lang in unique_languages if lang not in fixed_marker_map and lang is not None])
    unknown_marker_map = {lang: fallback_markers[i % len(fallback_markers)] for i, lang in enumerate(unknown_langs)}
    if 'unk' not in unknown_marker_map and 'unk' not in fixed_marker_map and 'unk' in unique_languages:
        unknown_marker_map['unk'] = fallback_markers[len(unknown_marker_map) % len(fallback_markers)]
    marker_map = {**fixed_marker_map, **unknown_marker_map}
    for group_name in ("honest", "fraud"):
        if group_name == "honest":
            keys = honest_keys
            lengths_dict = honest_lengths_dict
            distances_dict = honest_distances_dict
        else:
            keys = fraud_keys
            lengths_dict = fraud_lengths_dict
            distances_dict = fraud_distances_dict

        for k in keys:
            xs = lengths_dict[k]
            ys = distances_dict[k]
            if not xs:
                continue
            group_color = honest_color_by_key[k] if group_name == "honest" else fraud_color_by_key[k]
            group_langs = languages_groups[group_name][k]
            for lang in unique_languages:
                idxs = [i for i, l in enumerate(group_langs) if l == lang]
                if not idxs:
                    continue
                plt.scatter(
                    [xs[i] for i in idxs],
                    [ys[i] for i in idxs],
                    c=group_color,
                    marker=marker_map[lang],
                    alpha=0.6,
                    s=marker_size,
                    label=None,
                )
    if bounds is not None:
        if not (isinstance(bounds, (list, tuple)) and len(bounds) == 2):
            raise ValueError("bounds must be a tuple/list of two floats: (lower, upper)")
        lower, upper = bounds
        plt.axhline(lower, color='blue', linestyle='--', label='_nolegend_')
        plt.axhline(upper, color='purple', linestyle='--', label='_nolegend_')
    group_handles = []
    for k in honest_keys:
        group_handles.append(
            Line2D([0], [0], marker='o', color=honest_color_by_key[k], linestyle='None', markersize=8, label=f"Honest - {k}")
        )
    for k in fraud_keys:
        group_handles.append(
            Line2D([0], [0], marker='o', color=fraud_color_by_key[k], linestyle='None', markersize=8, label=f"Fraud - {k}")
        )
    legend1 = plt.legend(handles=group_handles, title='Groups', loc='upper left')
    plt.gca().add_artist(legend1)
    language_name_map = {'sp': 'Spanish', 'en': 'English', 'ch': 'Chinese', 'ar': 'Arabic', 'hi': 'Hindi', 'unk': 'Unknown'}
    lang_handles = [
        Line2D([0], [0], marker=marker_map[lang], color='black', linestyle='None', markersize=8,
               label=language_name_map.get(lang, str(lang)))
        for lang in (['sp', 'en', 'ch', 'ar', 'hi', 'unk'] if any(l in marker_map for l in ['sp','en','ch','ar','hi','unk']) else unique_languages)
        if lang in marker_map
    ]
    legend2 = plt.legend(handles=lang_handles, title='Languages', loc='upper right')
    if bounds is not None:
        plt.gca().add_artist(legend2)
        lower, upper = bounds
        bounds_handles = [
            Line2D([0], [0], color='blue', linestyle='--', linewidth=2, label=f'Lower: {lower:.6f}'),
            Line2D([0], [0], color='purple', linestyle='--', linewidth=2, label=f'Upper: {upper:.6f}')
        ]
        plt.legend(handles=bounds_handles, title='Bounds', loc='lower right')

    plt.title(f'{name} - Length vs Distance Comparison')
    plt.xlabel('Length (characters)')
    plt.ylabel('Distance')
    plt.grid(True, alpha=0.3)
    plt.tight_layout()
    if save_to is not None:
        safe_name = name.strip().replace(' ', '_').replace('/', '_').replace('\\', '_')
        if not safe_name:
            safe_name = "comparison"
        filename = f"{safe_name}_length_vs_distance.png"
        filepath = os.path.join(save_to, filename)
        plt.savefig(filepath, dpi=300, bbox_inches='tight')
        print(f"Saved comparison plot to: {filepath}")
    
    plt.show()


def generate_name_from_config(jsonl_path, model_short_name):
    if jsonl_path.endswith('.jsonl'):
        config_path = jsonl_path.replace('.jsonl', '_config.json')
    else:
        config_path = jsonl_path + '_config.json'
    
    if not os.path.exists(config_path):
        raise FileNotFoundError(f"Config file not found: {config_path}")
    
    with open(config_path, 'r') as f:
        config = json.load(f)
    
    model_inference = config['model_inference']['model']
    model_validation = config['model_validation']['model']
    is_honest = (model_inference == model_validation)
    honesty_label = 'honest' if is_honest else 'fraud'
    
    gpu_inference = config['server_inference']['gpu']
    gpu_validation = config['server_validation']['gpu']
    
    name = f"{honesty_label}_{model_short_name}_{gpu_inference}vs{gpu_validation}"
    
    return name


def plot_violin_comparison(distributions_dict, title="Distance Distributions", ylabel="Distance", figsize=(10, 6), show=True, ylim=None):
    if not isinstance(distributions_dict, dict) or not distributions_dict:
        print("Nothing to plot: empty or invalid input.")
        return

    group_names = []
    group_values = []

    for name, values in distributions_dict.items():
        arr = np.asarray(values, dtype=float)
        arr = arr[np.isfinite(arr)]
        if arr.size == 0:
            continue
        group_names.append(name)
        group_values.append(arr)

    if not group_values:
        print("Nothing to plot: all groups are empty after cleaning.")
        return

    fig, ax = plt.subplots(figsize=figsize)
    
    parts = ax.violinplot(group_values, positions=range(len(group_names)), 
                          showmeans=False, showmedians=True, showextrema=True)
    for pc in parts['bodies']:
        pc.set_facecolor('#8dd3c7')
        pc.set_alpha(0.7)
        pc.set_edgecolor('black')
        pc.set_linewidth(1)
    
    for partname in ('cbars', 'cmins', 'cmaxes', 'cmedians'):
        if partname in parts:
            vp = parts[partname]
            vp.set_edgecolor('black')
            vp.set_linewidth(1)
    ax.set_title(title)
    ax.set_xlabel("Group")
    ax.set_ylabel(ylabel)
    ax.set_xticks(range(len(group_names)))
    ax.set_xticklabels(group_names, rotation=45, ha='right')
    
    if ylim is not None:
        ax.set_ylim(ylim)
    
    plt.tight_layout()
    if show:
        plt.show()

    print("Mean and std per group:")
    means = {}
    stds = {}
    counts = {}
    for name, arr in zip(group_names, group_values):
        counts[name] = int(arr.size)
        means[name] = float(np.mean(arr)) if arr.size > 0 else float("nan")
        stds[name] = float(np.std(arr, ddof=1)) if arr.size > 1 else float("nan")
    for name in sorted(group_names, key=lambda k: means[k]):
        m = means[name]
        s = stds[name]
        n = counts[name]
        m_str = f"{m:.6f}" if np.isfinite(m) else "nan"
        s_str = f"{s:.6f}" if np.isfinite(s) else "nan"
        print(f"  {name}: n={n}, mean={m_str}, std={s_str}")

    if len(group_names) >= 2:
        max_mean_key = max(group_names, key=lambda k: means[k])
        min_mean_key = min(group_names, key=lambda k: means[k])
        if np.isfinite(means[max_mean_key]) and np.isfinite(means[min_mean_key]):
            delta_mean = means[max_mean_key] - means[min_mean_key]
            print(f"Delta mean: {delta_mean:.6f} ({min_mean_key} -> {max_mean_key})")

        finite_stds = {k: v for k, v in stds.items() if np.isfinite(v)}
        if len(finite_stds) >= 2:
            max_std_key = max(finite_stds, key=lambda k: finite_stds[k])
            min_std_key = min(finite_stds, key=lambda k: finite_stds[k])
            print(
                f"Std range: {finite_stds[min_std_key]:.6f} ({min_std_key}) .. "
                f"{finite_stds[max_std_key]:.6f} ({max_std_key})"
            )
