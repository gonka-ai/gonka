import csv
import json
import random
from pathlib import Path

from config import Z, environment, params_i as configured_params_i, s, REWARD_POOL

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
    return [
        [random.choice(environment["GPU"]) for node_k in range(environment["nodes"])]
        for H_j in range(environment["hosts"])
    ]


def init_params_i():
    return {M_i: values.copy() for M_i, values in configured_params_i.items()}


def init_coeff_i(params_i):
    return {M_i: values["coeff_i_min"] for M_i, values in params_i.items()}


def host_response(hardware_distribution, throughput, coeff_i):
    W_i_j = {}

    for H_j, nodes in enumerate(hardware_distribution):
        W_i_j[H_j] = {M_i: 0 for M_i in throughput}

        for GPU in nodes:
            M_i = max(
                throughput,
                key=lambda model: throughput[model][GPU] * coeff_i[model],
            )
            W_i_j[H_j][M_i] += throughput[M_i][GPU]

    return W_i_j


def get_share_i(W_i_j, params_i):
    normalized = {
        M_i: values["D_i"] * sum(W_i[M_i] for W_i in W_i_j.values())
        for M_i, values in params_i.items()
    }
    total = sum(normalized.values())
    return {M_i: value / total for M_i, value in normalized.items()}


def f(share_i, coeff_i, params_i, Z, s):
    next_coeff_i = {}

    for M_i, values in params_i.items():
        if values["T_i"] == 0:
            next_coeff_i[M_i] = values["coeff_i_min"]
            continue

        err_i = values["T_i"] - share_i[M_i]
        if abs(err_i) <= Z:
            next_coeff_i[M_i] = coeff_i[M_i]
        elif err_i > 0:
            next_coeff_i[M_i] = min(
                coeff_i[M_i] * (1 + s),
                values["coeff_i_max"],
            )
        else:
            next_coeff_i[M_i] = max(
                coeff_i[M_i] * (1 - s),
                values["coeff_i_min"],
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
        "s": s,
        "REWARD_POOL": REWARD_POOL,
        "epochs": [],
    }

    for N in range(environment["N"] + 1):
        W_i_j = host_response(hardware_distribution, throughput, coeff_i)
        share_i = get_share_i(W_i_j, params_i)
        W_j = {
            H_j: sum(W_i[M_i] * coeff_i[M_i] for M_i in throughput)
            for H_j, W_i in W_i_j.items()
        }
        total_W = sum(W_j.values())
        W_ref = throughput[BASE_MODEL]["H100"] * coeff_i[BASE_MODEL]
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
                "W_j": W_j,
                "rewards_j": rewards_j,
            }
        )

        coeff_i = f(share_i, coeff_i, params_i, Z, s)

    return experiment


def write_experiment(experiment):
    artifact = Path(__file__).parent / "experiment.json"
    artifact.write_text(json.dumps(experiment, indent=2) + "\n")
    return artifact


if __name__ == "__main__":
    print(write_experiment(simulate()))
