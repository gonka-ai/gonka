# Initializing dynamic PoC coefficients

This document describes how to initialize dynamic PoC coefficients from current chain parameters and measured model throughput. It defines coefficient bounds, relative difficulties, target shares, and the initial controller state.

MiniMax M2.7 is the base model with a fixed coefficient of `0.3024`. Its coefficient can stay fixed because the other models' coefficients adjust relative to it. Its allocation is the share left after allocation to the other models. Governance chooses target shares based on demand.

## 1. Current chain parameters

Mainnet coefficients at epoch `403` (2026-09-23):


| Model ID                             | Static coefficient |
| ------------------------------------ | ------------------ |
| `MiniMaxAI/MiniMax-M2.7`             | 0.3024             |
| `deepseek-ai/DeepSeek-V4-Flash-0731` | 0.246              |
| `zai-org/GLM-5.3-Flash`              | 0.62               |


Keeping the base coefficient at `0.3024` preserves existing weight units.

Source: [chain model parameters](data/snapshot-2026-09-23/chain_models.csv).

## 2. Measurements

Each value below is the number of nonces generated per minute by 8 GPUs of the specified class.


| Model / variant         | H100 | H200 | B200  | B300  |
| ----------------------- | ---- | ---- | ----- | ----- |
| MiniMax M2.7 FP8        | 4736 | 6912 | 10496 | 14336 |
| GLM 5.3 Flash           | 1775 | 2878 | 5454  | 8120  |
| DeepSeek V4 Flash FP8   | 3072 | 4864 | 9216  | 13824 |
| DeepSeek V4 Flash NVFP4 | -    | -    | 12800 | 22528 |


For each hardware class, use the fastest supported variant. For DeepSeek, this selects FP8 on H100/H200 and NVFP4 on B200/B300. The calculation assumes both variants can serve the same chain model. A dash means no measurement is available.

Source: [benchmark spreadsheet](https://docs.google.com/spreadsheets/d/1VYLtTM_M7XQO1un4GeOBfceLXx7r4XksN4VGUkrCuLs/edit?gid=0) and [saved measurements](data/snapshot-2026-09-23/benchmarks_8gpu.csv).

## 3. Exact formulas

Let `q[i,g]` be model `i` throughput per 8 GPUs of class `g`. Let `b` be MiniMax and `c_b = 0.3024` its fixed coefficient. Let `delta = 0.05` be the desired 5% switching incentive. Take minima and maxima over `g = {H100, H200, B200, B300}`.

The parity coefficient gives a hardware class equal weight on model `i` and MiniMax. Set the lower and upper bounds around the extreme parity values:

```text
parity[i,g] = c_b * q[b,g] / q[i,g]

coeff_min[i] = min_g(parity[i,g]) / (1 + delta)
coeff_max[i] = max_g(parity[i,g]) * (1 + delta)

D[i] = q[b,H100] / q[i,H100]

coeff_min[b] = coeff_max[b] = c_b
D[b] = 1
```

At parity, `q[i,g] * parity[i,g] = q[b,g] * c_b`. At the floor, MiniMax earns at least 5% more raw weight on every measured class. At the ceiling, model `i` earns at least 5% more on every measured class before dilution.

`D` converts each model's PoC weight to comparable compute units using an 8xH100 reference server. It depends only on measured throughput ratios.

For these measurements, B300 sets both non-base floors and H100 sets both ceilings:


| Model    | `D`           | `coeff_min`                       | `coeff_max`                     |
| -------- | ------------- | --------------------------------- | ------------------------------- |
| GLM      | `4736 / 1775` | `0.3024 * (14336 / 8120) / 1.05`  | `0.3024 * (4736 / 1775) * 1.05` |
| DeepSeek | `4736 / 3072` | `0.3024 * (14336 / 22528) / 1.05` | `0.3024 * (4736 / 3072) * 1.05` |


The resulting parameters, truncated to 12 fractional places, are:


| Model    | Current coeff (for reference) | `relative_difficulty` | `coeff_min`    | `coeff_max`    |
| -------- | ----------------------------- | --------------------- | -------------- | -------------- |
| MiniMax  | 0.3024                        | 1                     | 0.3024         | 0.3024         |
| GLM      | 0.62                          | 2.668169014084        | 0.508468965517 | 0.847197025352 |
| DeepSeek | 0.246                         | 1.541666666666        | 0.183272727272 | 0.48951        |




## 4. Targets and controller initialization

Governance sets `target_share_bps` for every enabled model, totaling `10000` basis points (100%). MiniMax still requires a target in the configuration. Setting `coeff_min = coeff_max = 0.3024` keeps its coefficient fixed. For an equal-share experiment only, use MiniMax `3334`, GLM `3333`, DeepSeek `3333`.

Targets apply to each model's share of difficulty-normalized PoC weight:

```text
share[i] = D[i] * total_poc_weight[i] / sum_k(D[k] * total_poc_weight[k])
```

The controller adjusts coefficients once per epoch using the previous epoch's shares. It holds a coefficient steady within 5 percentage points of its target. Use these defaults:

```text
target_zone_bps = 500
step_min = 0.005
step_max = 0.05
bootstrap_step_max = 0.25
bootstrap_share_bps = 100
```

A fresh simulation or newly enabled model starts with `base_coeff = coeff_min`, `s = step_max / 2 = 0.025`, and `prev_sign = 0`. Here `s` is the fractional adjustment step, and `prev_sign` records the previous adjustment direction.

For existing models, preserve their starting coefficients during rollout:

1. Enable dynamic configuration with each model's bounds pinned to its old static coefficient. The migration seeds `D = 1` and targets totaling `10000`. Pinned bounds keep adjustment and dilution inert.
2. Form an epoch with that configuration so the old coefficients are stored as dynamic controller state.
3. Apply the derived bounds and difficulties plus governance-selected targets. Carry existing controller state, clamp coefficients into the new bounds, and then apply the normal epoch adjustment. All three current static coefficients already lie within the derived bounds.

Configuration is frozen at PoC start. When a model exceeds its target share, the excess earns `coeff_min`, reducing the effective coefficient used for participant weights. A zero target pins that model's coefficient to its floor and resets the step/sign state.

See [the protocol](dynamic-coeff.md) for epoch adjustments and [the rollout rules](dynamic-coeff-impl.md) for migration details.

---



## 5. Simulation

We simulate 20 epochs with the epoch-403 hardware and the parameters above. Targets are 33.34% MiniMax, 33.33% GLM, and 33.33% DeepSeek. Epoch 0 is the starting allocation with the current static coefficients. Epoch 1 applies the first coefficient adjustment.

### Hardware and host behavior

The simulation includes 609 GPUs across 162 existing nodes owned by 24 hosts. Tensor parallel size (TP) is the number of GPUs needed for one model replica.


| GPU family | GPUs | MiniMax TP | GLM TP | DeepSeek TP |
| ---------- | ---- | ---------- | ------ | ----------- |
| H100       | 356  | 4          | 8      | 4           |
| H200       | 146  | 2          | 4      | 2           |
| B200       | 20   | 2          | 4      | 2           |
| B300       | 87   | 1          | 2      | 1           |


The 64 GPUs without benchmarks are excluded: 40 A100, 8 H20, 8 RTX PRO 6000, and 8 unidentified GPUs. H100 PCIe uses the H100 measurements, H200 NVL uses H200 measurements, and both B300 variants use B300 measurements.

- Hardware counts, ownership, and node sizes stay fixed. Initial assignments come from epoch-403 PoC results.
- A node runs as many replicas as fit its GPU count using the measured tensor parallel size. Its rate is `floor(node_GPUs / TP) * TP / 8 * q[i,g]`. A model requiring more GPUs than the node contains is unavailable.
- Hosts maximize their share of a common PoC reward pool: `GNK_host = reward_pool * host_weight / network_weight`.
- Hosts evaluate one node switch at a time. Each candidate is evaluated after recomputing dilution and the weights of all nodes owned by that host.
- A switch must increase host rewards by more than 1% of the switching node's current reward. This threshold represents redeployment cost.
- Every host responds each epoch. Nodes are visited in shuffled order with seed `0` until no profitable single-node switch remains. Coordinated switches of several nodes are outside this model.
- Rewards are proportional to PoC weight. Group caps, collateral adjustments, inference revenue, and redeployment downtime are excluded.



### Coefficients and compute shares

Each coefficient cell shows `base / effective`. The effective coefficient includes dilution and determines rewards. Shares use difficulty-normalized PoC throughput. The starting shares are calculated from the same benchmarks.


| Epoch | MiniMax coeff       | GLM coeff           | DeepSeek coeff      | MiniMax share | GLM share | DeepSeek share |
| ----- | ------------------- | ------------------- | ------------------- | ------------- | --------- | -------------- |
| 0     | 0.302400 / 0.302400 | 0.620000 / 0.620000 | 0.246000 / 0.246000 | 76.12%        | 4.43%     | 19.46%         |
| 1     | 0.302400 / 0.302400 | 0.651000 / 0.651000 | 0.258300 / 0.234936 | 45.25%        | 6.34%     | 48.40%         |
| 2     | 0.302400 / 0.302400 | 0.683550 / 0.683550 | 0.251843 / 0.234609 | 46.06%        | 9.42%     | 44.52%         |
| 3     | 0.302400 / 0.302400 | 0.717728 / 0.717728 | 0.239250 / 0.225182 | 46.06%        | 9.42%     | 44.52%         |
| 4     | 0.302400 / 0.302400 | 0.753614 / 0.753614 | 0.227288 / 0.216709 | 32.36%        | 23.77%    | 43.87%         |
| 5     | 0.302400 / 0.302400 | 0.791295 / 0.791295 | 0.215923 / 0.208076 | 32.36%        | 23.77%    | 43.87%         |
| 6     | 0.302400 / 0.302400 | 0.830859 / 0.802606 | 0.205127 / 0.199875 | 19.59%        | 36.53%    | 43.87%         |
| 7-20  | 0.302400 / 0.302400 | 0.830859 / 0.795415 | 0.194871 / 0.194798 | 29.01%        | 37.45%    | 33.54%         |




### GPUs assigned to each model

GPU types are reported by participants and are not independently verified. Different cards can produce equivalent PoC throughput, so measured compute power does not establish the exact GPU type.

Each cell lists individual GPU counts assigned to that model. Omitted GPU types have zero allocation. Epoch ranges share the same allocation.

| Epoch | MiniMax | GLM | DeepSeek |
| --- | --- | --- | --- |
| Pre-upgrade | 348 H100, 124 H200, 16 B200, 61 B300 | 8 H200, 4 B200, 4 B300 | 8 H100, 14 H200, 22 B300 |
| 1 | 356 H100, 146 H200 | 20 B200, 4 B300 | 83 B300 |
| 2-3 | 356 H100, 146 H200 | 20 B200, 12 B300 | 75 B300 |
| 4-5 | 356 H100, 34 H200 | 112 H200, 20 B200, 12 B300 | 75 B300 |
| 6 | 196 H100, 34 H200 | 160 H100, 112 H200, 20 B200, 12 B300 | 75 B300 |
| 7-20 | 220 H100, 34 H200, 22 B300 | 136 H100, 112 H200, 20 B200, 12 B300 | 53 B300 |

### **GNK per 8 GPUs relative to 8xH100**

This compares the average gross reward per 8 GPUs in each family with the average reward per 8 H100 GPUs in the same epoch. It uses the assignments above, including cases where a GPU family serves several models.

```text
weight_per_8[g] = 8 * sum(weight of nodes in family g) / GPU_count[g]
GNK_ratio[g] = weight_per_8[g] / weight_per_8[H100]
```

H100 is the reference and always has a value of `1.000`. For example, B200 has a value of `3.046` in epochs 7-20. If 8 H100 GPUs earn 100 GNK in one of those epochs, 8 B200 GPUs earn 304.6 GNK on average. The 100 GNK is an example amount.


Pre-upgrade uses the epoch-403 assignments and static coefficients, calculated with the same benchmarks and reward model.

| GPU | Pre-upgrade | Epoch 1 | Epoch 2 | Epoch 3 | Epoch 4 | Epoch 5 | Epoch 6 | Epochs 7-20 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| H100 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 |
| H200 | 1.403 | 1.459 | 1.459 | 1.459 | 1.502 | 1.560 | 1.581 | 1.575 |
| B200 | 2.269 | 2.479 | 2.603 | 2.733 | 2.870 | 3.013 | 3.064 | 3.046 |
| B300 | 3.298 | 3.695 | 3.716 | 3.615 | 3.528 | 3.440 | 3.346 | 3.272 |


All three shares enter the target zone at epoch 7. Allocations and coefficients stay unchanged through epoch 20. The final shares differ from one third because adjustment stops within 5 percentage points of each target.

Reproduce with `python3 proposals/multi-model-poc/simulation/epoch403.py`. [Full results](simulation/epoch403-results.json) include all 20 epochs, GPU allocations, controller state, and switching counts.
