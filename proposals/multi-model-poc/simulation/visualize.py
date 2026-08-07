import json
from collections import Counter
from pathlib import Path

import matplotlib.pyplot as plt

BASE_MODEL = "minimax-m2.7-fp8"


def selected_model(throughput, GPU, effective_coeff_i):
    return max(throughput, key=lambda M_i: throughput[M_i][GPU] * effective_coeff_i[M_i])


def visualize():
    experiment = json.loads(
        (Path(__file__).parent / "experiment.json").read_text()
    )
    epochs = experiment["epochs"]
    params_i = experiment["params_i"]
    throughput = experiment["throughput"]
    environment = experiment["environment"]
    N = [epoch["N"] for epoch in epochs]
    models = list(params_i)
    gpus = environment["GPU"]
    colors = {M_i: f"C{index}" for index, M_i in enumerate(models)}
    gpu_colors = {"H100": "#94a3b8", "H200": "#64748b", "B200": "#3b82f6", "B300": "#1d4ed8"}

    REWARD_POOL = experiment["REWARD_POOL"]
    gpu_rewards = {GPU: [] for GPU in gpus}
    for epoch in epochs:
        total_W = sum(epoch["W_j"].values())
        effective_coeff_i = epoch["effective_coeff_i"]
        W_ref = throughput[BASE_MODEL]["H100"] * effective_coeff_i[BASE_MODEL]
        reward_ref = (REWARD_POOL * W_ref / total_W) if total_W > 0 else 0.0
        for GPU in gpus:
            best_model = selected_model(throughput, GPU, effective_coeff_i)
            W_node = throughput[best_model][GPU] * effective_coeff_i[best_model]
            reward_per_node = (REWARD_POOL * W_node / total_W) if total_W > 0 else 0.0
            normalized_reward = (reward_per_node / reward_ref) if reward_ref > 0 else 0.0
            gpu_rewards[GPU].append(normalized_reward)

    figure = plt.figure(figsize=(14, 12))
    grid = figure.add_gridspec(
        4, 2, width_ratios=[1, 1.7], hspace=0.45, wspace=0.15
    )

    static_coeffs = []
    dynamic_models = []
    for M_i in models:
        values = [epoch["coeff_i"][M_i] for epoch in epochs]
        if min(values) == max(values):
            note = ""
            if values[0] == params_i[M_i]["coeff_i_min"]:
                note = " (pinned at min)"
            if params_i[M_i]["coeff_i_min"] == params_i[M_i]["coeff_i_max"]:
                note = " (fixed)"
            static_coeffs.append(f"  {M_i}: {values[0]:.4g}{note}")
        else:
            dynamic_models.append(M_i)

    panel = figure.add_subplot(grid[0, 0])
    panel.axis("off")
    total_nodes = sum(len(nodes) for nodes in experiment["hardware_distribution"])
    lines = [
        "Simulation parameters",
        f"  hosts={environment['hosts']}  nodes={total_nodes}  seed={environment['seed']}",
        f"  Z={experiment['Z']}  s_min={experiment['s_min']}  s_max={experiment['s_max']}",
        f"  s_max_bootstrap={experiment['s_max_bootstrap']}  epsilon={experiment['epsilon']}  epochs={environment['N']}",
        "Targets",
        *[f"  {M_i}: T_i={params_i[M_i]['T_i']:.2f}" for M_i in models],
        "",
        "Static coefficients",
        *static_coeffs,
    ]
    panel.text(
        0, 1, "\n".join(lines), va="top", family="monospace", fontsize=9
    )

    panel = figure.add_subplot(grid[1, 0])
    counts = Counter(
        GPU for nodes in experiment["hardware_distribution"] for GPU in nodes
    )
    power = {GPU: counts[GPU] * throughput[BASE_MODEL][GPU] for GPU in gpus}
    total = sum(power.values())
    shares = [power[GPU] / total for GPU in gpus]
    bar_colors = [gpu_colors[GPU] for GPU in gpus]
    bars = panel.barh(gpus, shares, color=bar_colors)
    for bar, GPU in zip(bars, gpus):
        panel.text(
            bar.get_width() + 0.01,
            bar.get_y() + bar.get_height() / 2,
            f"{power[GPU] / total:.0%} ({counts[GPU]} nodes)",
            va="center",
            fontsize=8,
        )
    panel.set_xlim(0, max(shares) * 1.35)
    panel.invert_yaxis()
    panel.set_title(f"network compute by GPU type\n({BASE_MODEL} units)", fontsize=9)

    panel = figure.add_subplot(grid[2, 0])
    cumulative_gpu_rewards = {GPU: sum(gpu_rewards[GPU]) for GPU in gpus}
    max_reward = max(cumulative_gpu_rewards.values())
    bar_colors = [gpu_colors[GPU] for GPU in gpus]
    bars = panel.barh(gpus, [cumulative_gpu_rewards[GPU] for GPU in gpus], color=bar_colors)
    for bar, GPU in zip(bars, gpus):
        panel.text(
            bar.get_width() + (max_reward * 0.01),
            bar.get_y() + bar.get_height() / 2,
            f"{cumulative_gpu_rewards[GPU]:.1f}",
            va="center",
            fontsize=8,
        )
    panel.set_xlim(0, max_reward * 1.35)
    panel.invert_yaxis()
    panel.set_title("cumulative reward per single GPU\n(relative to 8xH100)", fontsize=9)

    panel = figure.add_subplot(grid[3, 0])
    panel.axis("off")
    lines = ["GPU preferences (from run)", ""]
    for GPU in gpus:
        chosen = [
            selected_model(throughput, GPU, epoch["effective_coeff_i"]) for epoch in epochs
        ]
        if len(set(chosen)) > 1:
            lines.append(f"  {GPU}: switches {' <-> '.join(sorted(set(chosen)))}")
            continue
        best = chosen[0]
        effective_coeff_i = epochs[-1]["effective_coeff_i"]
        rewards = {M_i: throughput[M_i][GPU] * effective_coeff_i[M_i] for M_i in models}
        runner = max((M_i for M_i in models if M_i != best), key=rewards.get)
        ratio = rewards[runner] / rewards[best]
        tie = "  TIE" if ratio == 1 else ""
        lines.append(f"  {GPU}: {best}")
        lines.append(f"        runner-up {runner} at {ratio:.0%}{tie}")
    panel.text(
        0, 1, "\n".join(lines), va="top", family="monospace", fontsize=9
    )

    panel = figure.add_subplot(grid[0, 1])
    for M_i in models:
        share_i = [epoch["share_i"][M_i] for epoch in epochs]
        panel.plot(N, share_i, color=colors[M_i], label=M_i)
    zones = {
        (values["T_i"] - experiment["Z"], values["T_i"] + experiment["Z"])
        for values in params_i.values()
    }
    for low, high in zones:
        panel.axhspan(low, high, color="gray", alpha=0.2)
    panel.set_ylim(0, 1)
    panel.set_ylabel("share_i")
    panel.set_title("model compute shares, target zone shaded")
    panel.legend(fontsize=8)

    other_models = [M_i for M_i in models if M_i != BASE_MODEL]
    for row, M_i in enumerate(other_models, start=1):
        panel = figure.add_subplot(grid[row, 1])
        coeff_ratio = [
            epoch["coeff_i"][M_i] / epoch["coeff_i"][BASE_MODEL]
            for epoch in epochs
        ]
        effective_coeff_ratio = [
            epoch["effective_coeff_i"][M_i] / epoch["effective_coeff_i"][BASE_MODEL]
            for epoch in epochs
        ]
        panel.plot(N, coeff_ratio, color=colors[M_i], linestyle=":", label=f"coeff {M_i} / {BASE_MODEL}")
        panel.plot(N, effective_coeff_ratio, color=colors[M_i], label=f"effective_coeff {M_i} / {BASE_MODEL}")
        thresholds = {
            GPU: throughput[BASE_MODEL][GPU] / throughput[M_i][GPU]
            for GPU in gpus
        }
        for GPU, threshold in thresholds.items():
            panel.axhline(threshold, color="gray", linestyle="--", linewidth=0.8)
            panel.text(
                N[-1], threshold, f" {GPU}: {threshold:.3f}",
                va="center", fontsize=8, color="gray",
            )
        if max(effective_coeff_ratio) > min(effective_coeff_ratio):
            inset = panel.inset_axes([0.35, 0.45, 0.6, 0.45])
            inset.plot(N, effective_coeff_ratio, color=colors[M_i])
            active = min(
                thresholds.values(),
                key=lambda t: abs(t - sum(effective_coeff_ratio) / len(effective_coeff_ratio)),
            )
            inset.axhline(active, color="gray", linestyle="--", linewidth=0.8)
            margin = (max(effective_coeff_ratio) - min(effective_coeff_ratio)) * 0.3
            inset.set_ylim(
                min(effective_coeff_ratio + [active]) - margin,
                max(effective_coeff_ratio + [active]) + margin,
            )
            inset.set_title("zoom at active threshold", fontsize=8)
            inset.tick_params(labelsize=7)
        panel.set_ylabel("coefficient ratio")
        panel.set_title(
            f"{M_i} switching thresholds: crossing a line flips that GPU type",
            fontsize=9,
        )
        panel.legend(fontsize=8, loc="center left")

    panel = figure.add_subplot(grid[3, 1])
    for GPU in gpus:
        panel.plot(N, gpu_rewards[GPU], color=gpu_colors[GPU], label=f"{GPU} reward")
    panel.set_ylabel("relative to 8xH100")
    panel.set_title("rewards per single GPU type over epochs (normalized to 8xH100)", fontsize=9)
    panel.legend(fontsize=8)
    panel.set_xlabel("N")

    name = environment.get("name")
    suffix = f"-{name}" if name else ""
    artifact = Path(__file__).parent / f"dynamic-coeff{suffix}.png"
    figure.savefig(artifact, bbox_inches="tight", dpi=110)
    return artifact


if __name__ == "__main__":
    print(visualize())
