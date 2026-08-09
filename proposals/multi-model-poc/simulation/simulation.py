import csv
import json
import random
from pathlib import Path

from config import (
    REWARD_POOL,
    Z,
    bootstrap_share,
    environment,
    epsilon,
    params_i as configured_params_i,
    s_max,
    s_max_bootstrap,
    s_min,
)

BASE_MODEL = "minimax-m2.7-fp8"


def load_throughput():
    throughput = {}

    with (Path(__file__).parent.parent / "data" / "models.csv").open() as file:
        for row in csv.DictReader(file):
            M_i = row["model"]
            if M_i.startswith("deepseek-v4-flash"):
                M_i = "deepseek-v4-flash"

            GPU = row["GPU"]
            value = int(row["nonces/min(8GPU)"])
            throughput.setdefault(M_i, {})
            throughput[M_i][GPU] = max(throughput[M_i].get(GPU, 0), value)

    return throughput


def init_hardware_distribution(environment):
    random.seed(environment["seed"])
    if "gpu_counts" in environment:
        # Explicit node counts per GPU type, dealt round-robin across hosts.
        pool = [
            GPU
            for GPU, count in environment["gpu_counts"].items()
            for _ in range(count)
        ]
        random.shuffle(pool)
        distribution = [[] for H_j in range(environment["hosts"])]
        for index, GPU in enumerate(pool):
            distribution[index % environment["hosts"]].append(GPU)
        return distribution
    return [
        [random.choice(environment["GPU"]) for node_k in range(environment["nodes"])]
        for H_j in range(environment["hosts"])
    ]


def init_params_i():
    return {M_i: values.copy() for M_i, values in configured_params_i.items()}


def init_coeff_i(params_i):
    return {M_i: values["coeff_i_min"] for M_i, values in params_i.items()}


# effective_coeff[i] = R_i / share_i (doc: Protocol); share_i = 0 keeps the
# full coefficient so a new model can attract its first hosts.
def get_effective_coeff(share_i, coeff_i, params_i):
    effective_coeff = {}
    for M_i, values in params_i.items():
        sh = share_i[M_i]
        c = coeff_i[M_i]
        c_min = values["coeff_i_min"]
        T_i = values["T_i"]
        if sh <= 0:
            effective_coeff[M_i] = c
        else:
            R_i = c * min(sh, T_i) + c_min * max(0.0, sh - T_i)
            effective_coeff[M_i] = R_i / sh
    return effective_coeff


def compute_W_i_j(allocation, hardware_distribution, throughput):
    hosts = len(hardware_distribution)
    W_i_j = {H_j: {M_i: 0.0 for M_i in throughput} for H_j in range(hosts)}
    for H_j, nodes in enumerate(hardware_distribution):
        for node_idx, GPU in enumerate(nodes):
            model = allocation[H_j][node_idx]
            W_i_j[H_j][model] += throughput[model][GPU]
    return W_i_j


# Best-response dynamics (doc: Host response): nodes move one at a time
# against recomputed shares and coefficients until no node gains more than
# epsilon by switching.
def find_stable_allocation(hardware_distribution, throughput, coeff_i, params_i, prev_allocation=None):
    if prev_allocation is not None:
        allocation = [[model for model in host_nodes] for host_nodes in prev_allocation]
    else:
        allocation = [[BASE_MODEL for _ in range(len(host_nodes))] for host_nodes in hardware_distribution]

    # Initialize running weights per model
    total_model_weight = {M_i: 0.0 for M_i in throughput}
    for H_j, nodes in enumerate(hardware_distribution):
        for node_idx, GPU in enumerate(nodes):
            model = allocation[H_j][node_idx]
            total_model_weight[model] += throughput[model][GPU]

    # Flatten nodes to a list of (H_j, node_idx, GPU)
    nodes_list = []
    for H_j, host_gpus in enumerate(hardware_distribution):
        for node_idx, GPU in enumerate(host_gpus):
            nodes_list.append((H_j, node_idx, GPU))

    # Fixed cap on passes guarantees termination (doc: Host response).
    max_passes = 100
    for pass_idx in range(max_passes):
        # Random order avoids artifacts from a fixed node ordering.
        random.shuffle(nodes_list)
        any_moved = False

        for H_j, node_idx, GPU in nodes_list:
            current_model = allocation[H_j][node_idx]

            normalized = {
                M_i: values["D_i"] * total_model_weight[M_i]
                for M_i, values in params_i.items()
            }
            total_norm = sum(normalized.values())
            if total_norm == 0:
                share_i = {M_i: 0.0 for M_i in params_i}
            else:
                share_i = {M_i: val / total_norm for M_i, val in normalized.items()}

            effective_coeff = get_effective_coeff(share_i, coeff_i, params_i)

            best_model = max(
                throughput.keys(),
                key=lambda model: throughput[model][GPU] * effective_coeff[model]
            )

            best_val = throughput[best_model][GPU] * effective_coeff[best_model]
            current_val = throughput[current_model][GPU] * effective_coeff[current_model]
            if best_val > current_val * (1 + epsilon):
                allocation[H_j][node_idx] = best_model
                total_model_weight[current_model] -= throughput[current_model][GPU]
                total_model_weight[best_model] += throughput[best_model][GPU]
                any_moved = True

        if not any_moved:
            break

    return allocation


def get_share_i(W_i_j, params_i):
    normalized = {
        M_i: values["D_i"] * sum(W_i[M_i] for W_i in W_i_j.values())
        for M_i, values in params_i.items()
    }
    total = sum(normalized.values())
    return {M_i: value / total for M_i, value in normalized.items()}


# New models start with s = s_max / 2 and prev_sign = 0 (doc: Design of f).
def init_f_state(params_i):
    return {M_i: {"s": s_max / 2, "prev_sign": 0} for M_i in params_i}


def f(share_i, coeff_i, params_i, Z, state):
    next_coeff_i = {}

    for M_i, values in params_i.items():
        if values["T_i"] == 0:
            next_coeff_i[M_i] = values["coeff_i_min"]
            continue

        st = state[M_i]
        err_i = values["T_i"] - share_i[M_i]
        if abs(err_i) <= Z:
            st["s"], st["prev_sign"] = s_max / 2, 0
            next_coeff_i[M_i] = coeff_i[M_i]
            continue

        cap = s_max_bootstrap if share_i[M_i] < bootstrap_share else s_max
        sign = 1 if err_i > 0 else -1
        if sign == st["prev_sign"] or st["prev_sign"] == 0:
            st["s"] = min(2 * st["s"], cap)
        else:
            st["s"] = min(max(st["s"] / 2, s_min), cap)
        st["prev_sign"] = sign

        next_coeff_i[M_i] = min(
            max(coeff_i[M_i] * (1 + sign * st["s"]), values["coeff_i_min"]),
            values["coeff_i_max"],
        )

    return next_coeff_i


def simulate():
    throughput = load_throughput()
    hardware_distribution = init_hardware_distribution(environment)
    params_i = init_params_i()
    coeff_i = init_coeff_i(params_i)

    experiment = {
        "environment": environment,
        "hardware_distribution": hardware_distribution,
        "params_i": params_i,
        "throughput": throughput,
        "Z": Z,
        "s_min": s_min,
        "s_max": s_max,
        "s_max_bootstrap": s_max_bootstrap,
        "bootstrap_share": bootstrap_share,
        "epsilon": epsilon,
        "REWARD_POOL": REWARD_POOL,
        "epochs": [],
    }

    f_state = init_f_state(params_i)
    allocation = None
    for N in range(environment["N"] + 1):
        allocation = find_stable_allocation(hardware_distribution, throughput, coeff_i, params_i, allocation)
        W_i_j = compute_W_i_j(allocation, hardware_distribution, throughput)
        share_i = get_share_i(W_i_j, params_i)
        effective_coeff_i = get_effective_coeff(share_i, coeff_i, params_i)
        W_j = {
            H_j: sum(W_i[M_i] * effective_coeff_i[M_i] for M_i in throughput)
            for H_j, W_i in W_i_j.items()
        }
        # Reporting only, not part of the protocol: reward per host normalized
        # to a single 8xH100 node running the base model.
        total_W = sum(W_j.values())
        W_ref = throughput[BASE_MODEL]["H100"] * effective_coeff_i[BASE_MODEL]
        reward_ref = (REWARD_POOL * W_ref / total_W) if total_W > 0 else 0.0
        rewards_j = {
            H_j: ((REWARD_POOL * w / total_W) / reward_ref) if reward_ref > 0 else 0.0
            for H_j, w in W_j.items()
        }

        experiment["epochs"].append(
            {
                "N": N,
                "share_i": share_i,
                "coeff_i": coeff_i,
                "effective_coeff_i": effective_coeff_i,
                "W_j": W_j,
                "rewards_j": rewards_j,
            }
        )

        coeff_i = f(share_i, coeff_i, params_i, Z, f_state)

    return experiment


def write_experiment(experiment):
    artifact = Path(__file__).parent / "experiment.json"
    artifact.write_text(json.dumps(experiment, indent=2) + "\n")
    return artifact


if __name__ == "__main__":
    print(write_experiment(simulate()))
