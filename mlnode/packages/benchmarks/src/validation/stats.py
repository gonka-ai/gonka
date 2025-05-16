"""
stats.py: A small library for descriptive statistics, distribution fitting, and visualization.

Usage Example:
--------------
    import stats

    # Suppose 'distances' is your dataset.
    stats.describe_data(distances, name="distances")

    # Fit & select best distribution
    best_fit, all_results = stats.select_best_fit(distances)
    print("Best distribution:", best_fit.dist_name, "with KS stat:", best_fit.ks_stat)

    # Plot real data vs. the best distribution
    stats.plot_real_vs_fitted(distances, dist_name=best_fit.dist_name, bins=50)

    # Sample from the best distribution
    samples = stats.sample_from_fit(best_fit, n=1000)
"""

import numpy as np
import pandas as pd
import scipy.stats as st
import matplotlib.pyplot as plt
import seaborn as sns

from typing import Dict, Tuple, Optional, Sequence
from pydantic import BaseModel


class FittedDistribution(BaseModel):
    dist_name: str
    ks_stat: Optional[float]
    p_val: Optional[float]
    fit_params: Optional[Tuple[float, ...]]

    def __str__(self) -> str:
        params_str = ""
        if self.fit_params:
            params_str = f", params={self.fit_params}"
        
        stats_str = ""
        if self.ks_stat is not None and self.p_val is not None:
            stats_str = f", KS={self.ks_stat:.4f}, p={self.p_val:.4f}"
            
        return f"FittedDistribution(dist={self.dist_name}{params_str}{stats_str})"


def describe_data(data: Sequence[float], name: str = "variable") -> None:
    """
    Print basic descriptive statistics (count, min, max, mean, std, quartiles).
    """
    arr = np.array(data)
    series = pd.Series(arr)
    print(f"\n--- {name.upper()} DESCRIPTIVE STATISTICS ---")
    print(f"Count: {len(arr)}")
    print(f"Min:   {arr.min():.4f}")
    print(f"Max:   {arr.max():.4f}")
    print(f"Mean:  {arr.mean():.4f}")
    print(f"Std:   {arr.std(ddof=1):.4f}")
    print("Quartiles:")
    print(series.quantile([0.25, 0.50, 0.75]))


def safe_beta_fit(data: Sequence[float], loc: float = 0, scale: float = 1, eps: float = 1e-6):
    """
    Fit a Beta distribution when data may include 0 or 1.
    Returns alpha, beta, loc, scale.
    """
    arr = np.array(data)
    arr_clipped = np.clip(arr, eps, 1 - eps)
    alpha_, beta_, loc_, scale_ = st.beta.fit(arr_clipped, floc=loc, fscale=scale)
    return alpha_, beta_, loc_, scale_


def safe_gamma_fit(data: Sequence[float], eps: float = 1e-9):
    """
    Fit a Gamma distribution when data may include zeros.
    Returns shape, loc, scale, data_shifted.
    """
    arr = np.array(data)
    data_shifted = arr + eps
    shape, loc, scale = st.gamma.fit(data_shifted, floc=0)
    return shape, loc, scale, data_shifted


def safe_lognorm_fit(data: Sequence[float], eps: float = 1e-9):
    """
    Fit a Lognormal distribution when data may include zeros.
    Returns shape, loc, scale, data_shifted.
    """
    arr = np.array(data)
    data_shifted = arr + eps
    shape, loc, scale = st.lognorm.fit(data_shifted, floc=0)
    return shape, loc, scale, data_shifted


def fit_and_report(data: Sequence[float], dist_name: str = "normal") -> FittedDistribution:
    """
    Fit a distribution, print parameters and KS test, and return a FittedDistribution object.
    """
    arr = np.array(data)

    if dist_name == "normal":
        mu, sigma = st.norm.fit(arr)
        ks_stat, p_val = st.kstest(arr, "norm", args=(mu, sigma))
        print(f"Fitted Normal Params: mu={mu:.4f}, sigma={sigma:.4f}")
        print(f"KS test: statistic={ks_stat:.4f}, p-value={p_val:.4f}")
        return FittedDistribution(dist_name="normal", ks_stat=ks_stat, p_val=p_val, fit_params=(mu, sigma))

    elif dist_name == "gamma":
        shape, loc, scale, data_shifted = safe_gamma_fit(arr)
        ks_stat, p_val = st.kstest(data_shifted, "gamma", args=(shape, loc, scale))
        print(f"Fitted Gamma Params: shape={shape:.4f}, loc={loc:.4f}, scale={scale:.4f}")
        print(f"KS test: statistic={ks_stat:.4f}, p-value={p_val:.4f}")
        return FittedDistribution(dist_name="gamma", ks_stat=ks_stat, p_val=p_val, fit_params=(shape, loc, scale))

    elif dist_name == "lognorm":
        shape, loc, scale, data_shifted = safe_lognorm_fit(arr)
        ks_stat, p_val = st.kstest(data_shifted, "lognorm", args=(shape, loc, scale))
        print(f"Fitted Lognormal Params: shape={shape:.4f}, loc={loc:.4f}, scale={scale:.4f}")
        print(f"KS test: statistic={ks_stat:.4f}, p-value={p_val:.4f}")
        return FittedDistribution(dist_name="lognorm", ks_stat=ks_stat, p_val=p_val, fit_params=(shape, loc, scale))

    elif dist_name == "beta":
        alpha_, beta_, loc_, scale_ = safe_beta_fit(arr)
        arr_clipped = np.clip(arr, 1e-6, 1 - 1e-6)
        ks_stat, p_val = st.kstest(arr_clipped, "beta", args=(alpha_, beta_, loc_, scale_))
        print(f"Fitted Beta Params: alpha={alpha_:.4f}, beta={beta_:.4f}, loc={loc_:.4f}, scale={scale_:.4f}")
        print(f"KS test: statistic={ks_stat:.4f}, p-value={p_val:.4f}")
        return FittedDistribution(dist_name="beta", ks_stat=ks_stat, p_val=p_val, fit_params=(alpha_, beta_, loc_, scale_))

    else:
        print(f"Distribution '{dist_name}' not implemented.")
        return FittedDistribution(dist_name="unknown", ks_stat=None, p_val=None, fit_params=None)


def select_best_fit(
    data: Sequence[float],
    distributions: Tuple[str, ...] = ("normal", "gamma", "lognorm", "beta")
) -> Tuple[Optional[FittedDistribution], Dict[str, FittedDistribution]]:
    """
    Evaluate multiple distributions using a KS test, print a report, and return:
      1) The best-fitting FittedDistribution
      2) A dict of all FittedDistribution results keyed by name.
    """
    best_stat = float("inf")
    best_dist: Optional[FittedDistribution] = None
    results: Dict[str, FittedDistribution] = {}

    print("#" * 80)
    for dist_name in distributions:
        print(f"Fitting {dist_name}...")
        fit_result = fit_and_report(data, dist_name)
        results[dist_name] = fit_result
        if fit_result.ks_stat is not None and fit_result.ks_stat < best_stat:
            best_stat = fit_result.ks_stat
            best_dist = fit_result
    print("#" * 80)
    print(f"Best distribution: {best_dist}")
    print("#" * 80)

    return best_dist, results


def plot_real_vs_fitted(data: Sequence[float], dist_name: str, bins: int = 50) -> None:
    """
    Compare real data vs. a chosen fitted distribution with
    a histogram/KDE, ECDF, and Q-Q plot.
    """
    fit_result = fit_and_report(data, dist_name)
    arr = np.array(data)

    if fit_result.fit_params is None:
        raise ValueError(f"Cannot plot distribution '{dist_name}': no fit parameters.")

    if fit_result.dist_name == "normal":
        mu, sigma = fit_result.fit_params
        fitted_samples = st.norm.rvs(mu, sigma, size=len(arr))
    elif fit_result.dist_name == "gamma":
        shape, loc, scale = fit_result.fit_params
        fitted_samples = st.gamma.rvs(shape, loc=loc, scale=scale, size=len(arr))
    elif fit_result.dist_name == "lognorm":
        shape, loc, scale = fit_result.fit_params
        fitted_samples = st.lognorm.rvs(shape, loc=loc, scale=scale, size=len(arr))
    elif fit_result.dist_name == "beta":
        alpha_, beta_, loc_, scale_ = fit_result.fit_params
        fitted_samples = st.beta.rvs(alpha_, beta_, loc=loc_, scale=scale_, size=len(arr))
    else:
        raise ValueError(f"Unknown distribution '{dist_name}'.")

    fig, axes = plt.subplots(1, 3, figsize=(18, 5))

    sns.histplot(
        arr,
        bins=bins,
        kde=True,
        stat="density",
        color="blue",
        alpha=0.3,
        ax=axes[0],
        label="Real Data"
    )
    sns.histplot(
        fitted_samples,
        bins=bins,
        kde=True,
        stat="density",
        color="red",
        alpha=0.3,
        ax=axes[0],
        label=f"{dist_name.title()} Samples"
    )
    axes[0].set_title(f"Histogram/KDE: Real vs. {dist_name.title()}")
    axes[0].legend()

    sorted_data = np.sort(arr)
    yvals_data = np.arange(1, len(sorted_data) + 1) / len(sorted_data)
    axes[1].plot(sorted_data, yvals_data, label="Real Data (ECDF)", color="blue")

    sorted_fitted = np.sort(fitted_samples)
    yvals_fitted = np.arange(1, len(sorted_fitted) + 1) / len(sorted_fitted)
    axes[1].plot(sorted_fitted, yvals_fitted, label=f"{dist_name.title()} (ECDF)", color="red")
    axes[1].set_title("ECDF Comparison")
    axes[1].set_xlabel("Value")
    axes[1].set_ylabel("Cumulative Probability")
    axes[1].legend()

    axes[2].scatter(sorted_data, sorted_fitted, alpha=0.3)
    max_val = max(sorted_data.max(), sorted_fitted.max())
    axes[2].plot([0, max_val], [0, max_val], color="gray", linestyle="--")
    axes[2].set_title("Q-Q Plot: Real vs. Fitted")
    axes[2].set_xlabel("Real Data Quantiles")
    axes[2].set_ylabel(f"{dist_name.title()} Sample Quantiles")

    plt.tight_layout()
    plt.show()


def sample_from_fit(fit_result: FittedDistribution, n: int = 1000) -> np.ndarray:
    """
    Generate random samples from a FittedDistribution.
    """
    if fit_result.fit_params is None:
        raise ValueError(f"No fit parameters to sample from for distribution '{fit_result.dist_name}'.")

    if fit_result.dist_name == "normal":
        mu, sigma = fit_result.fit_params
        return st.norm.rvs(mu, sigma, size=n)
    elif fit_result.dist_name == "gamma":
        shape, loc, scale = fit_result.fit_params
        return st.gamma.rvs(shape, loc=loc, scale=scale, size=n)
    elif fit_result.dist_name == "lognorm":
        shape, loc, scale = fit_result.fit_params
        return st.lognorm.rvs(shape, loc=loc, scale=scale, size=n)
    elif fit_result.dist_name == "beta":
        alpha_, beta_, loc_, scale_ = fit_result.fit_params
        return st.beta.rvs(alpha_, beta_, loc=loc_, scale=scale_, size=n)
    else:
        raise ValueError(f"Unknown distribution '{fit_result.dist_name}'.")