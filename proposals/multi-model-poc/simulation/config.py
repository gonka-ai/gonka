environment = {
    "hosts": 30,
    "nodes": 5,
    "GPU": ["H100", "H200", "B200", "B300"],
    "seed": 0,
    "N": 100,
}

params_i = {
    "kimi-k2.6": {
        "coeff_i_min": 2.342857142857143,
        "coeff_i_max": 6.814388489208633,
        "D_i": 6.814388489208633,
        "T_i": 1 / 3,
    },
    "minimax-m2.7-fp8": {
        "coeff_i_min": 1,
        "coeff_i_max": 1,
        "D_i": 1.0,
        "T_i": 1 / 3,
    },
    "deepseek-v4-flash": {
        "coeff_i_min": 0.6363636363636364,
        "coeff_i_max": 1.5416666666666667,
        "D_i": 1.5416666666666667,
        "T_i": 1 / 3,
    },
}

Z = 0.05
s = 0.01
REWARD_POOL = 300000

