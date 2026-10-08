# Synthetic environment: each node gets a random GPU type.
environment_random = {
    "hosts": 30,
    "nodes": 5,
    "GPU": ["H100", "H200", "B200", "B300"],
    "seed": 0,
    "N": 100,
}

# Mainnet-like distribution from epoch 352 explorer data. Servers per row =
# total GPUs // 8; unknown GPU types dropped (H20, A100, RTX PRO 6000, H800).
# H200 217//8=27 (+ H200 NVL 2//8=0), B200 134//8=16,
# H100 124//8=15 + H100 PCIe 12//8=1, B300 20//8=2. Total 61 nodes.
environment_epoch_352 = {
    "name": "epoch-352",
    "hosts": 12,
    "GPU": ["H100", "H200", "B200", "B300"],
    "gpu_counts": {"H100": 16, "H200": 27, "B200": 16, "B300": 2},
    "seed": 0,
    "N": 100,
}

environment = environment_epoch_352

# Bounds follow the ~5% rule: coeff_i_min sits 5% below the parity of the most
# efficient class (switch-back incentive), coeff_i_max sits 5% above the parity
# of the least efficient class the model should be able to attract.
params_i = {
    "kimi-k2.6": {
        "coeff_i_min": 10496 / 4480 / 1.05,  # B200 parity / 1.05
        "coeff_i_max": 4736 / 695 * 1.05,  # H100 parity * 1.05
        "D_i": 4736 / 695,
        "T_i": 1 / 3,
    },
    "minimax-m2.7-fp8": {
        "coeff_i_min": 1,
        "coeff_i_max": 1,
        "D_i": 1.0,
        "T_i": 1 / 3,
    },
    "deepseek-v4-flash": {
        "coeff_i_min": 14336 / 22528 / 1.05,  # B300 parity / 1.05
        "coeff_i_max": 4736 / 3072 * 1.05,  # H100 parity * 1.05
        "D_i": 4736 / 3072,
        "T_i": 1 / 3,
    },
}

Z = 0.05

# Adaptive step: halves on sign flips to settle on parity points, doubles back
# afterward, never exceeding s_max. The reset to s_max / 2 makes the first
# adjustment after launch or a freeze double to exactly the default s_max.
# Bootstrap models (share < bootstrap_share) get the higher cap so the doubling
# continues the initial climb (5% -> 10% -> 20% -> 25%).
s_min = 0.005
s_max = 0.05
s_max_bootstrap = 0.25
bootstrap_share = 0.01

# Redeployment cost threshold: a host switches a node only when the expected
# gain exceeds epsilon (doc: 1-2%). Must exceed the single-node coefficient
# impact or best-response dynamics cycle (observed in simulation, not a
# formal guarantee); the pass cap in find_stable_allocation guarantees
# termination.
epsilon = 0.01

REWARD_POOL = 300000
