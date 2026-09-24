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

The simulation includes 609 reported GPUs across 162 existing nodes owned by 24 hosts.

These scenarios use spreadsheet throughput scaled by reported GPU counts. They are not calibrated to observed chain PoC weights. For the same 162 nodes, the saved chain weights normalized with section 3's difficulties give starting shares of 82.92% MiniMax, 3.82% GLM, and 13.26% DeepSeek. The original benchmark model gives 76.12%, 4.43%, and 19.46%. Reward ratios and convergence times therefore describe the stated benchmark assumptions rather than a calibrated mainnet forecast.


| GPU family | Reported GPUs |
| --- | ---: |
| H100 | 356 |
| H200 | 146 |
| B200 | 20 |
| B300 | 87 |


The 64 GPUs without benchmarks are excluded: 40 A100, 8 H20, 8 RTX PRO 6000, and 8 unidentified GPUs. H100 PCIe uses the H100 measurements, H200 NVL uses H200 measurements, and both B300 variants use B300 measurements.

- Hardware counts, ownership, and node sizes stay fixed. Initial assignments come from epoch-403 PoC results.
- Each node represents normalized compute capacity. Its rate on model `i` is `reported_GPU_count / 8 * q[i,g]`. Every modeled node can switch to any of the three models. Reported counts do not establish physical topology or tensor-parallel deployment limits.
- Hosts maximize their share of a common PoC reward pool: `GNK_host = reward_pool * host_weight / network_weight`.
- Hosts evaluate one node switch at a time. Each candidate is evaluated after recomputing dilution and the weights of all nodes owned by that host.
- A switch must increase host rewards by more than 1% of the switching node's current reward. This threshold represents redeployment cost.
- Every host responds each epoch. Nodes are visited in shuffled order with seed `0` until no profitable single-node switch remains. Coordinated switches of several nodes are outside this model.
- Rewards are proportional to PoC weight. Group caps, collateral adjustments, inference revenue, and redeployment downtime are excluded.



### Coefficients and compute shares

Each coefficient cell shows `base / effective`. The effective coefficient includes dilution and determines rewards. Shares use difficulty-normalized PoC throughput. The starting shares are calculated from the same benchmarks.


| Epoch | MiniMax coeff | GLM coeff | DeepSeek coeff | MiniMax share | GLM share | DeepSeek share |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Pre-upgrade | 0.302400 / 0.302400 | 0.620000 / 0.620000 | 0.246000 / 0.246000 | 76.12% | 4.43% | 19.46% |
| 1 | 0.302400 / 0.302400 | 0.651000 / 0.651000 | 0.258300 / 0.241392 | 46.37% | 10.60% | 43.03% |
| 2 | 0.302400 / 0.302400 | 0.683550 / 0.683550 | 0.251843 / 0.251230 | 48.33% | 18.04% | 33.63% |
| 3 | 0.302400 / 0.302400 | 0.717728 / 0.713885 | 0.251843 / 0.251843 | 52.51% | 33.95% | 13.53% |
| 4 | 0.302400 / 0.302400 | 0.717728 / 0.717728 | 0.264435 / 0.263709 | 48.33% | 18.04% | 33.63% |
| 5 | 0.302400 / 0.302400 | 0.753614 / 0.735694 | 0.264435 / 0.264435 | 39.72% | 35.96% | 24.32% |
| 6-20 | 0.302400 / 0.302400 | 0.753614 / 0.746501 | 0.277656 / 0.276919 | 32.08% | 34.33% | 33.59% |




### GPUs assigned to each model

GPU types are reported by participants and are not independently verified. Different cards can produce equivalent PoC throughput, so measured compute power does not establish the exact GPU type.

Each cell lists individual GPU counts assigned to that model. Omitted GPU types have zero allocation. Epoch ranges share the same allocation.

| Epoch | MiniMax | GLM | DeepSeek |
| --- | --- | --- | --- |
| Pre-upgrade | 348 H100, 124 H200, 16 B200, 61 B300 | 8 H200, 4 B200, 4 B300 | 8 H100, 14 H200, 22 B300 |
| 1 | 356 H100, 146 H200 | 20 B200, 15 B300 | 72 B300 |
| 2 | 356 H100, 146 H200 | 20 B200, 33 B300 | 54 B300 |
| 3 | 356 H100, 146 H200 | 20 B200, 67 B300 | 20 B300 |
| 4 | 356 H100, 146 H200 | 20 B200, 33 B300 | 54 B300 |
| 5 | 356 H100, 68 H200 | 78 H200, 20 B200, 49 B300 | 38 B300 |
| 6-20 | 356 H100, 20 H200 | 126 H200, 20 B200, 32 B300 | 55 B300 |

### **GNK per 8 GPUs relative to 8xH100**

This compares the average gross reward per 8 GPUs in each family with the average reward per 8 H100 GPUs in the same epoch. It uses the assignments above, including cases where a GPU family serves several models.

```text
weight_per_8[g] = 8 * sum(weight of nodes in family g) / GPU_count[g]
GNK_ratio[g] = weight_per_8[g] / weight_per_8[H100]
```

H100 is the reference and always has a value of `1.000`. In epochs 6-20, B300 has a value of `4.311`. If 8 H100 GPUs earn 100 GNK in one of those epochs, 8 B300 GPUs earn about 431.1 GNK on average. The 100 GNK is an example amount.


The table shows selected epochs. Pre-upgrade uses the epoch-403 assignments and static coefficients, calculated with the same benchmarks and reward model.

| GPU | Pre-upgrade | Epoch 1 | Epoch 5 | Epochs 6-20 |
| --- | ---: | ---: | ---: | ---: |
| H100 | 1.000 | 1.000 | 1.000 | 1.000 |
| H200 | 1.403 | 1.459 | 1.470 | 1.495 |
| B200 | 2.269 | 2.479 | 2.802 | 2.843 |
| B300 | 3.298 | 3.779 | 4.166 | 4.311 |


All shares enter their target zones at epoch 6 and remain unchanged through epoch 20: MiniMax 32.08%, GLM 34.33%, and DeepSeek 33.59%.

Reproduce with `python3 proposals/multi-model-poc/simulation/epoch403.py`. [Full results](simulation/epoch403-results.json) include all 20 epochs, GPU allocations, controller state, and switching counts.

---

## 6. Simulation: MiniMax 10%, DeepSeek 45%, GLM 45%

This scenario runs 20 epochs from the same epoch-403 assignments and static coefficients. Targets are MiniMax `1000`, DeepSeek `4500`, and GLM `4500` basis points. GLM uses the GLM 5.3 Flash measurements above. Hardware, coefficient bounds, controller settings, and host behavior are unchanged.

### Coefficients and compute shares

Each coefficient cell shows `base / effective`. The effective coefficient includes dilution and determines rewards.

| Epoch | MiniMax coeff | GLM coeff | DeepSeek coeff | MiniMax share | GLM share | DeepSeek share |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Pre-upgrade | 0.302400 / 0.302400 | 0.620000 / 0.620000 | 0.246000 / 0.246000 | 76.12% | 4.43% | 19.46% |
| 1 | 0.302400 / 0.302400 | 0.651000 / 0.651000 | 0.258300 / 0.250402 | 44.86% | 4.84% | 50.29% |
| 2 | 0.302400 / 0.302400 | 0.683550 / 0.683550 | 0.251843 / 0.251824 | 45.96% | 9.03% | 45.01% |
| 3 | 0.302400 / 0.302400 | 0.717728 / 0.717728 | 0.251843 / 0.251843 | 55.33% | 44.67% | 0.00% |
| 4 | 0.302400 / 0.302400 | 0.717728 / 0.717728 | 0.264435 / 0.264413 | 45.96% | 9.03% | 45.01% |
| 5 | 0.302400 / 0.302400 | 0.753614 / 0.741876 | 0.264435 / 0.264435 | 35.10% | 47.26% | 17.64% |
| 6 | 0.302400 / 0.302400 | 0.753614 / 0.753614 | 0.277656 / 0.277377 | 28.09% | 26.78% | 45.13% |
| 7 | 0.302400 / 0.302400 | 0.791295 / 0.772476 | 0.277656 / 0.277656 | 31.22% | 48.21% | 20.58% |
| 8 | 0.302400 / 0.302400 | 0.791295 / 0.791295 | 0.291539 / 0.291539 | 28.15% | 27.20% | 44.65% |
| 9 | 0.302400 / 0.302400 | 0.830859 / 0.799532 | 0.291539 / 0.291539 | 22.10% | 49.84% | 28.05% |
| 10-20 | 0.302400 / 0.302400 | 0.830859 / 0.814276 | 0.306116 / 0.306116 | 7.91% | 47.44% | 44.65% |

### GPUs assigned to each model

Counts are individual GPUs, using the same participant-reported hardware types. Omitted GPU types have zero allocation.

| Epoch | MiniMax | GLM | DeepSeek |
| --- | --- | --- | --- |
| Pre-upgrade | 348 H100, 124 H200, 16 B200, 61 B300 | 8 H200, 4 B200, 4 B300 | 8 H100, 14 H200, 22 B300 |
| 1 | 356 H100, 146 H200 | 20 B200 | 87 B300 |
| 2 | 356 H100, 146 H200 | 20 B200, 11 B300 | 76 B300 |
| 3 | 356 H100, 146 H200 | 20 B200, 87 B300 | None |
| 4 | 356 H100, 146 H200 | 20 B200, 11 B300 | 76 B300 |
| 5 | 356 H100, 26 H200 | 120 H200, 20 B200, 60 B300 | 27 B300 |
| 6 | 356 H100 | 146 H200, 20 B200, 9 B300 | 78 B300 |
| 7 | 356 H100 | 146 H200, 20 B200, 55 B300 | 32 B300 |
| 8 | 356 H100 | 146 H200, 20 B200, 10 B300 | 77 B300 |
| 9 | 260 H100 | 96 H100, 146 H200, 20 B200, 42 B300 | 45 B300 |
| 10-20 | 100 H100 | 256 H100, 146 H200, 20 B200, 10 B300 | 77 B300 |

### GNK per 8 GPUs relative to 8xH100

The table shows selected epochs. Each value compares average rewards per 8 GPUs with average rewards per 8 H100 GPUs in the same epoch. Pre-upgrade uses the starting assignments and static coefficients.

| GPU | Pre-upgrade | Epoch 1 | Epoch 5 | Epochs 10-20 |
| --- | ---: | ---: | ---: | ---: |
| H100 | 1.000 | 1.000 | 1.000 | 1.000 |
| H200 | 1.403 | 1.459 | 1.485 | 1.626 |
| B200 | 2.269 | 2.479 | 2.825 | 3.081 |
| B300 | 3.298 | 3.939 | 4.192 | 4.761 |

All shares enter their target zones at epoch 10 and remain unchanged through epoch 20: MiniMax 7.91%, GLM 47.44%, and DeepSeek 44.65%.

Reproduce from the repository root. Target arguments are ordered MiniMax, GLM, DeepSeek:

```bash
python3 proposals/multi-model-poc/simulation/epoch403.py \
  --target-bps 1000 4500 4500 \
  --output proposals/multi-model-poc/simulation/epoch403-minimax10-results.json
```

[Full results](simulation/epoch403-minimax10-results.json) include all 20 epochs.

---

## 7. Decode simulation: equal target shares

This scenario uses the spreadsheet's `Decode 8 GPUs nonces/min` column for node throughput, coefficient bounds, and relative difficulty. Targets remain 33.34% MiniMax, 33.33% GLM, and 33.33% DeepSeek. Starting coefficients remain MiniMax `0.3024`, GLM `0.62`, and DeepSeek `0.246`. The 609 GPUs, initial assignments, node sizes, ownership, and host behavior are unchanged.

### Decode measurements and parameters

Throughput is nonces/min per 8 GPUs. Node rates scale proportionally to reported GPU counts using the same capacity model as section 5.

| Model / variant | H100 | H200 | B200 | B300 |
| --- | ---: | ---: | ---: | ---: |
| MiniMax M2.7 FP8 | 5188 | 6832 | 13711 | 14368 |
| GLM 5.3 Flash | 921 | 1702 | 4202 | 5050 |
| DeepSeek V4 Flash FP8 | 2700 | 5488 | 11088 | 16376 |
| DeepSeek V4 Flash NVFP4 | - | - | 11152 | 17183 |

Source: [decode benchmark spreadsheet](https://docs.google.com/spreadsheets/d/1G7_U5JjWo6OJ3pDx0JN30sxJKW0nDhKnOD_O2c0jB5Q/edit?usp=sharing) and [saved measurements](data/benchmarks-decode-8gpu.csv).

Use the formulas in section 3 with these decode rates and `c_b = 0.3024`. DeepSeek again selects FP8 on H100/H200 and NVFP4 on B200/B300. B300 sets both lower bounds, and H100 sets both upper bounds.

| Model | Starting coeff | `relative_difficulty` | `coeff_min` | `coeff_max` |
| --- | ---: | ---: | ---: | ---: |
| MiniMax | 0.3024 | 1 | 0.3024 | 0.3024 |
| GLM | 0.62 | 5.633007600434 | 0.819402772277 | 1.788592573289 |
| DeepSeek | 0.246 | 1.921481481481 | 0.240818483384 | 0.6101088 |

GLM starts below its new lower bound. In epoch 1, the controller clamps it from `0.62` to `0.819402772277`, then applies the 5% increase to reach `0.860372910891`. Epoch 0 evaluates the original assignments with decode throughput and the unchanged static coefficients.

### Coefficients and compute shares

Each coefficient cell shows `base / effective`. The effective coefficient includes dilution and determines rewards. Shares use decode throughput normalized by the decode relative difficulties.

| Epoch | MiniMax coeff | GLM coeff | DeepSeek coeff | MiniMax share | GLM share | DeepSeek share |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 (decode start) | 0.302400 / 0.302400 | 0.620000 / 0.620000 | 0.246000 / 0.246000 | 75.74% | 5.76% | 18.50% |
| 1 | 0.302400 / 0.302400 | 0.860373 / 0.860373 | 0.258300 / 0.257041 | 63.05% | 1.03% | 35.92% |
| 2 | 0.302400 / 0.302400 | 0.903392 / 0.893514 | 0.258300 / 0.258300 | 55.21% | 37.77% | 7.01% |
| 3 | 0.302400 / 0.302400 | 0.903392 / 0.903392 | 0.271215 / 0.269904 | 53.07% | 12.10% | 34.83% |
| 4 | 0.302400 / 0.302400 | 0.948561 / 0.943883 | 0.271215 / 0.271215 | 54.95% | 34.58% | 10.47% |
| 5 | 0.302400 / 0.302400 | 0.948561 / 0.948561 | 0.284776 / 0.282880 | 53.07% | 12.10% | 34.83% |
| 6 | 0.302400 / 0.302400 | 0.995989 / 0.989593 | 0.284776 / 0.284776 | 54.95% | 34.58% | 10.47% |
| 7 | 0.302400 / 0.302400 | 0.995989 / 0.995989 | 0.299015 / 0.298661 | 51.86% | 14.60% | 33.53% |
| 8 | 0.302400 / 0.302400 | 1.045789 / 1.029962 | 0.299015 / 0.299015 | 48.01% | 35.84% | 16.16% |
| 9 | 0.302400 / 0.302400 | 1.045789 / 1.045789 | 0.313965 / 0.313182 | 46.81% | 19.50% | 33.69% |
| 10 | 0.302400 / 0.302400 | 1.098078 / 1.082318 | 0.313965 / 0.313965 | 47.97% | 35.33% | 16.70% |
| 11 | 0.302400 / 0.302400 | 1.098078 / 1.098078 | 0.329664 / 0.329664 | 46.85% | 19.98% | 33.17% |
| 12 | 0.302400 / 0.302400 | 1.152982 / 1.138696 | 0.329664 / 0.329664 | 47.93% | 34.82% | 17.25% |
| 13 | 0.302400 / 0.302400 | 1.152982 / 1.152982 | 0.346147 / 0.346147 | 46.85% | 19.98% | 33.17% |
| 14 | 0.302400 / 0.302400 | 1.210631 / 1.205071 | 0.346147 / 0.346147 | 47.86% | 33.81% | 18.33% |
| 15 | 0.302400 / 0.302400 | 1.210631 / 1.210631 | 0.363454 / 0.363454 | 46.85% | 19.98% | 33.17% |
| 16 | 0.302400 / 0.302400 | 1.271163 / 1.230714 | 0.363454 / 0.363454 | 39.78% | 36.61% | 23.61% |
| 17-20 | 0.302400 / 0.302400 | 1.271163 / 1.240614 | 0.381627 / 0.378324 | 30.12% | 35.75% | 34.13% |

### GPUs assigned to each model

Counts are individual GPUs using participant-reported hardware types. Omitted GPU types have zero allocation. Epoch ranges have identical allocations.

| Epoch | MiniMax | GLM | DeepSeek |
| --- | --- | --- | --- |
| Pre-upgrade | 348 H100, 124 H200, 16 B200, 61 B300 | 8 H200, 4 B200, 4 B300 | 8 H100, 14 H200, 22 B300 |
| 1 | 356 H100, 146 H200, 20 B200, 25 B300 | 2 B300 | 60 B300 |
| 2 | 356 H100, 146 H200, 20 B200 | 75 B300 | 12 B300 |
| 3 | 356 H100, 146 H200, 20 B200 | 25 B300 | 62 B300 |
| 4 | 356 H100, 146 H200, 20 B200 | 69 B300 | 18 B300 |
| 5 | 356 H100, 146 H200, 20 B200 | 25 B300 | 62 B300 |
| 6 | 356 H100, 146 H200, 20 B200 | 69 B300 | 18 B300 |
| 7 | 356 H100, 146 H200, 16 B200 | 4 B200, 27 B300 | 60 B300 |
| 8 | 356 H100, 146 H200 | 20 B200, 58 B300 | 29 B300 |
| 9 | 356 H100, 146 H200 | 20 B200, 25 B300 | 62 B300 |
| 10 | 356 H100, 146 H200 | 20 B200, 57 B300 | 30 B300 |
| 11 | 356 H100, 146 H200 | 20 B200, 26 B300 | 61 B300 |
| 12 | 356 H100, 146 H200 | 20 B200, 56 B300 | 31 B300 |
| 13 | 356 H100, 146 H200 | 20 B200, 26 B300 | 61 B300 |
| 14 | 356 H100, 146 H200 | 20 B200, 54 B300 | 33 B300 |
| 15 | 356 H100, 146 H200 | 20 B200, 26 B300 | 61 B300 |
| 16 | 356 H100, 88 H200 | 58 H200, 20 B200, 43 B300 | 44 B300 |
| 17-20 | 356 H100, 14 H200 | 108 H200, 20 B200, 28 B300 | 24 H200, 59 B300 |

### GNK per 8 GPUs relative to 8xH100

Pre-upgrade retains the original throughput measurements and static coefficients. Decode start uses decode throughput with those same coefficients and assignments. Subsequent columns show selected epochs. Full results include every epoch. Each ratio uses average H100 rewards in its own epoch as the reference.

| GPU | Pre-upgrade | Decode start | Epoch 1 | Epoch 5 | Epoch 10 | Epoch 15 | Epochs 17-20 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| H100 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 |
| H200 | 1.403 | 1.254 | 1.317 | 1.317 | 1.317 | 1.317 | 1.339 |
| B200 | 2.269 | 2.479 | 2.643 | 2.643 | 2.899 | 3.243 | 3.323 |
| B300 | 3.298 | 2.751 | 2.801 | 3.085 | 3.468 | 3.956 | 4.095 |

All shares enter their target zones at epoch 17 and remain unchanged through epoch 20: MiniMax 30.12%, GLM 35.75%, and DeepSeek 34.13%.

Reproduce from the repository root:

```bash
python3 proposals/multi-model-poc/simulation/epoch403.py \
  --decode \
  --output proposals/multi-model-poc/simulation/epoch403-decode-results.json
```

[Full results](simulation/epoch403-decode-results.json) include coefficients, GPU allocations, and reward ratios for every epoch.

---

## 8. Decode simulation: MiniMax 10%, DeepSeek 45%, GLM 45%

This scenario runs 30 epochs using the same decode measurements, derived bounds, and relative difficulties as section 7. Targets are MiniMax 10%, DeepSeek 45%, and GLM 45%. It starts from the original epoch-403 assignments and coefficients, with MiniMax `0.3024`, GLM `0.62`, and DeepSeek `0.246`.

### Coefficients and compute shares

Each coefficient cell shows `base / effective`. The effective coefficient includes dilution and determines rewards. Shares use decode throughput normalized by the decode relative difficulties.

| Epoch | MiniMax coeff | GLM coeff | DeepSeek coeff | MiniMax share | GLM share | DeepSeek share |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 (decode start) | 0.302400 / 0.302400 | 0.620000 / 0.620000 | 0.246000 / 0.246000 | 75.74% | 5.76% | 18.50% |
| 1 | 0.302400 / 0.302400 | 0.860373 / 0.860373 | 0.258300 / 0.257803 | 53.68% | 0.00% | 46.32% |
| 2 | 0.302400 / 0.302400 | 0.946410 / 0.946410 | 0.258300 / 0.258300 | 55.75% | 44.25% | 0.00% |
| 3 | 0.302400 / 0.302400 | 0.946410 / 0.946410 | 0.271215 / 0.271215 | 55.75% | 44.25% | 0.00% |
| 4 | 0.302400 / 0.302400 | 0.946410 / 0.946410 | 0.298337 / 0.294803 | 52.05% | 0.00% | 47.95% |
| 5 | 0.302400 / 0.302400 | 0.993731 / 0.993731 | 0.298337 / 0.298257 | 50.99% | 3.94% | 45.06% |
| 6 | 0.302400 / 0.302400 | 1.043417 / 1.035386 | 0.298337 / 0.298337 | 48.80% | 46.67% | 4.53% |
| 7 | 0.302400 / 0.302400 | 1.043417 / 1.043417 | 0.313253 / 0.313253 | 46.05% | 9.05% | 44.90% |
| 8 | 0.302400 / 0.302400 | 1.095588 / 1.094956 | 0.313253 / 0.313253 | 48.68% | 45.10% | 6.22% |
| 9 | 0.302400 / 0.302400 | 1.095588 / 1.095588 | 0.328916 / 0.328916 | 46.05% | 9.05% | 44.90% |
| 10 | 0.302400 / 0.302400 | 1.150368 / 1.149611 | 0.328916 / 0.328916 | 48.68% | 45.10% | 6.22% |
| 11 | 0.302400 / 0.302400 | 1.150368 / 1.150368 | 0.345362 / 0.345362 | 46.05% | 9.05% | 44.90% |
| 12 | 0.302400 / 0.302400 | 1.207886 / 1.206997 | 0.345362 / 0.345362 | 48.68% | 45.10% | 6.22% |
| 13 | 0.302400 / 0.302400 | 1.207886 / 1.207886 | 0.362630 / 0.362630 | 46.05% | 9.05% | 44.90% |
| 14 | 0.302400 / 0.302400 | 1.268280 / 1.235675 | 0.362630 / 0.362630 | 34.00% | 48.52% | 17.47% |
| 15 | 0.302400 / 0.302400 | 1.268280 / 1.268280 | 0.380761 / 0.380761 | 28.02% | 28.41% | 43.57% |
| 16 | 0.302400 / 0.302400 | 1.331694 / 1.295602 | 0.380761 / 0.380761 | 28.88% | 48.41% | 22.71% |
| 17 | 0.302400 / 0.302400 | 1.331694 / 1.331694 | 0.399799 / 0.399799 | 28.02% | 28.41% | 43.57% |
| 18 | 0.302400 / 0.302400 | 1.398279 / 1.362873 | 0.399799 / 0.399799 | 28.86% | 47.93% | 23.21% |
| 19 | 0.302400 / 0.302400 | 1.398279 / 1.398279 | 0.419789 / 0.419789 | 28.02% | 28.41% | 43.57% |
| 20 | 0.302400 / 0.302400 | 1.468193 / 1.428510 | 0.419789 / 0.419789 | 28.86% | 47.93% | 23.21% |
| 21 | 0.302400 / 0.302400 | 1.468193 / 1.468193 | 0.440779 / 0.440779 | 28.02% | 28.41% | 43.57% |
| 22 | 0.302400 / 0.302400 | 1.541602 / 1.497430 | 0.440779 / 0.440779 | 28.86% | 47.93% | 23.21% |
| 23 | 0.302400 / 0.302400 | 1.541602 / 1.541602 | 0.462818 / 0.462818 | 28.02% | 28.41% | 43.57% |
| 24 | 0.302400 / 0.302400 | 1.618683 / 1.569796 | 0.462818 / 0.462818 | 28.86% | 47.93% | 23.21% |
| 25 | 0.302400 / 0.302400 | 1.618683 / 1.618683 | 0.485959 / 0.485959 | 28.02% | 28.41% | 43.57% |
| 26 | 0.302400 / 0.302400 | 1.699617 / 1.651132 | 0.485959 / 0.485959 | 28.17% | 47.62% | 24.21% |
| 27 | 0.302400 / 0.302400 | 1.699617 / 1.699617 | 0.510257 / 0.510257 | 27.39% | 29.04% | 43.57% |
| 28 | 0.302400 / 0.302400 | 1.784598 / 1.699088 | 0.510257 / 0.510257 | 18.25% | 49.37% | 32.38% |
| 29-30 | 0.302400 / 0.302400 | 1.784598 / 1.716876 | 0.535769 / 0.533814 | 6.30% | 48.40% | 45.30% |

### GPUs assigned to each model

Counts are individual GPUs using participant-reported hardware types. Omitted GPU types have zero allocation. Epoch ranges have identical allocations.

| Epoch | MiniMax | GLM | DeepSeek |
| --- | --- | --- | --- |
| Pre-upgrade | 348 H100, 124 H200, 16 B200, 61 B300 | 8 H200, 4 B200, 4 B300 | 8 H100, 14 H200, 22 B300 |
| 1 | 356 H100, 146 H200, 20 B200, 4 B300 | None | 83 B300 |
| 2-3 | 356 H100, 146 H200, 20 B200 | 87 B300 | None |
| 4 | 356 H100, 146 H200, 20 B200 | None | 87 B300 |
| 5 | 356 H100, 146 H200, 16 B200 | 4 B200, 5 B300 | 82 B300 |
| 6 | 356 H100, 146 H200 | 20 B200, 79 B300 | 8 B300 |
| 7 | 356 H100, 146 H200 | 20 B200, 3 B300 | 84 B300 |
| 8 | 356 H100, 146 H200 | 20 B200, 76 B300 | 11 B300 |
| 9 | 356 H100, 146 H200 | 20 B200, 3 B300 | 84 B300 |
| 10 | 356 H100, 146 H200 | 20 B200, 76 B300 | 11 B300 |
| 11 | 356 H100, 146 H200 | 20 B200, 3 B300 | 84 B300 |
| 12 | 356 H100, 146 H200 | 20 B200, 76 B300 | 11 B300 |
| 13 | 356 H100, 146 H200 | 20 B200, 3 B300 | 84 B300 |
| 14 | 356 H100, 40 H200 | 106 H200, 20 B200, 54 B300 | 33 B300 |
| 15 | 356 H100 | 146 H200, 20 B200 | 87 B300 |
| 16 | 356 H100 | 146 H200, 20 B200, 43 B300 | 44 B300 |
| 17 | 356 H100 | 146 H200, 20 B200 | 87 B300 |
| 18 | 356 H100 | 146 H200, 20 B200, 42 B300 | 45 B300 |
| 19 | 356 H100 | 146 H200, 20 B200 | 87 B300 |
| 20 | 356 H100 | 146 H200, 20 B200, 42 B300 | 45 B300 |
| 21 | 356 H100 | 146 H200, 20 B200 | 87 B300 |
| 22 | 356 H100 | 146 H200, 20 B200, 42 B300 | 45 B300 |
| 23 | 356 H100 | 146 H200, 20 B200 | 87 B300 |
| 24 | 356 H100 | 146 H200, 20 B200, 42 B300 | 45 B300 |
| 25 | 356 H100 | 146 H200, 20 B200 | 87 B300 |
| 26 | 348 H100 | 8 H100, 146 H200, 20 B200, 40 B300 | 47 B300 |
| 27 | 348 H100 | 8 H100, 146 H200, 20 B200 | 87 B300 |
| 28 | 228 H100 | 128 H100, 138 H200, 20 B200, 26 B300 | 8 H200, 61 B300 |
| 29-30 | 80 H100 | 276 H100, 98 H200, 20 B200, 12 B300 | 48 H200, 75 B300 |

### GNK per 8 GPUs relative to 8xH100

Pre-upgrade retains the original throughput measurements and static coefficients. Decode start uses decode throughput with those same coefficients and assignments. Subsequent columns show selected epochs. Full results include every epoch. Each ratio uses average H100 rewards in its own epoch as the reference.

| GPU | Pre-upgrade | Decode start | Epoch 1 | Epoch 5 | Epoch 10 | Epoch 15 | Epoch 20 | Epoch 25 | Epoch 28 | Epochs 29-30 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| H100 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 |
| H200 | 1.403 | 1.254 | 1.317 | 1.317 | 1.317 | 1.376 | 1.550 | 1.756 | 1.842 | 1.853 |
| B200 | 2.269 | 2.479 | 2.643 | 2.647 | 3.079 | 3.397 | 3.826 | 4.335 | 4.555 | 4.570 |
| B300 | 3.298 | 2.751 | 2.821 | 3.263 | 3.688 | 4.170 | 4.598 | 5.323 | 5.558 | 5.767 |

All shares enter their target zones at epoch 29: MiniMax 6.30%, GLM 48.40%, and DeepSeek 45.30%. Epoch 30 confirms unchanged coefficients and allocations.

### Why convergence takes longer

Extending the same simulation to 100 epochs reaches all target zones at epoch 29. Coefficients and allocations remain unchanged through epoch 100.

At epoch 20, all 356 H100 GPUs still serve MiniMax. With that epoch's effective coefficients, weight per minute per 8 H100 GPUs is 1,569 on MiniMax, 1,316 on GLM, and 1,133 on DeepSeek. The competing coefficients must rise further to attract H100 capacity away from MiniMax.

GLM and DeepSeek meanwhile take turns attracting B300 capacity. Increasing one coefficient pulls GPUs from the other model, whose coefficient then increases in the next epoch. This alternation slows the rise toward rewards that can attract H100s.

By epoch 29, GLM attracts 276 H100 GPUs, leaving 80 on MiniMax:

| Model | Target share | Epoch 29 share |
| --- | ---: | ---: |
| MiniMax | 10% | 6.30% |
| GLM | 45% | 48.40% |
| DeepSeek | 45% | 45.30% |

Convergence means entering each target's tolerance of 5 percentage points. The controller holds coefficients steady within those zones, so the final shares need not equal 10% / 45% / 45% exactly.

Reproduce from the repository root. Target arguments are ordered MiniMax, GLM, DeepSeek:

```bash
python3 proposals/multi-model-poc/simulation/epoch403.py \
  --decode --target-bps 1000 4500 4500 --epochs 30 \
  --output proposals/multi-model-poc/simulation/epoch403-decode-minimax10-results.json
```

[Full results](simulation/epoch403-decode-minimax10-results.json) include coefficients, GPU allocations, and reward ratios for all 30 epochs.

Reproduce the 100-epoch convergence check:

```bash
python3 proposals/multi-model-poc/simulation/epoch403.py \
  --decode --target-bps 1000 4500 4500 --epochs 100 \
  --output /tmp/epoch403-decode-minimax10-100-results.json
```
