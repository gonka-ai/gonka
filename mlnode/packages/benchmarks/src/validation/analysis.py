import numpy as np
from sklearn.metrics import f1_score
import matplotlib.pyplot as plt
from collections import Counter
from tqdm import tqdm
from joblib import Parallel, delayed

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


def plot_length_vs_distance_comparison(name, honest_items, honest_distances, fraud_items, fraud_distances):
    """Create combined length vs distance plot for comparison"""
    # Calculate lengths for honest and fraud items
    honest_lengths = [len(item.inference_result.text) for item in honest_items]
    fraud_lengths = [len(item.inference_result.text) for item in fraud_items]

    # Combined overlay plot only
    plt.figure(figsize=(10, 6))
    plt.scatter(honest_lengths, honest_distances, alpha=0.5, color='blue', label='Honest Items', s=10)
    plt.scatter(fraud_lengths, fraud_distances, alpha=0.5, color='red', label='Fraud Items', s=10)
    plt.title(f'{name} - Length vs Distance Comparison')
    plt.xlabel('Length (characters)')
    plt.ylabel('Distance')
    plt.legend()
    plt.grid(True, alpha=0.3)
    plt.show()
