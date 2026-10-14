# Target configuration for dynamic PoC coefficients

This document defines the target coefficient bounds and relative difficulties from measured model throughput. It also describes target shares, controller defaults, and a simulation of this configuration.

Activation has two phases. The [v0.2.16 upgrade](../governance-artifacts/update-v0.2.16/README.md#dynamic-coefficients-v1) starts dynamic coefficients from the existing static values, with ranges limited to 10% above or below them. This document describes the proposed second phase, in which governance replaces those initial ranges with the full benchmark-derived ranges and updates relative difficulties. The simulation starts from the current first-phase state.

MiniMax M2.7 is the base model with a fixed coefficient of `0.3024`. Its coefficient can stay fixed because the other models' coefficients adjust relative to it. Its allocation is the share left after allocation to the other models. Governance chooses target shares based on demand.

## 1. Current chain parameters

Mainnet coefficients at epoch `424` (2026-10-14, 04:10 UTC, block `6545695`):


| Model ID                             | Base / effective coefficient |
| ------------------------------------ | ------------------ |
| `MiniMaxAI/MiniMax-M2.7`             | 0.3024 / 0.3024 |
| `deepseek-ai/DeepSeek-V4-Flash-0731` | 0.2706 / 0.2706 |
| `zai-org/GLM-5.3-Flash`              | 0.682 / 0.682 |


Keeping the base coefficient at `0.3024` preserves existing weight units.

The current allowed ranges are `0.558 - 0.682` for GLM and `0.2214 - 0.2706` for DeepSeek. MiniMax is fixed.

Source: [epoch coefficients](https://node3.gonka.ai/chain-api/productscience/inference/inference/dynamic_coefficients/424) and [saved model parameters](data/epoch424/models.csv).

## 2. Measurements

Each value below is the number of nonces generated per minute by 8 GPUs of the specified class, using the `mlnode-4.0.0-prefill` measurements.


| Model / variant         | H100 | H200 | B200  | B300  |
| ----------------------- | ---- | ---- | ----- | ----- |
| MiniMax M2.7 FP8        | 5800 | 10400 | 28000 | 34000 |
| GLM 5.3 Flash           | 2000 | 3300 | 6000  | 9200  |
| DeepSeek V4 Flash FP8   | 3400 | 7200 | 9216  | 13824 |
| DeepSeek V4 Flash NVFP4 | -    | -    | 15600 | 25600 |


For each hardware class, use the fastest supported variant. For DeepSeek, this selects FP8 on H100/H200 and NVFP4 on B200/B300. The calculation assumes both variants can serve the same chain model. A dash means no measurement is available.

Source: `mlnode-4.0.0-prefill`, column `8xGPUs nonces/min`, and [saved measurements](data/mlnode-4.0.0-prefill.csv).

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

At parity, `q[i,g] * parity[i,g] = q[b,g] * c_b`. At the floor, MiniMax earns at least 5% more PoC weight before dilution on every measured class. At the ceiling, model `i` earns at least 5% more on every measured class before dilution.

`D` converts each model's PoC weight to comparable compute units using an 8xH100 reference server. It depends only on measured throughput ratios.

For GLM, H100 has the lowest parity coefficient and determines the lower bound. For DeepSeek, B300 determines the lower bound. B200 has the highest parity coefficient for both models and determines both upper bounds:


| Model    | `D`           | `coeff_min`                       | `coeff_max`                     |
| -------- | ------------- | --------------------------------- | ------------------------------- |
| GLM      | `5800 / 2000` | `0.3024 * (5800 / 2000) / 1.05`  | `0.3024 * (28000 / 6000) * 1.05` |
| DeepSeek | `5800 / 3400` | `0.3024 * (34000 / 25600) / 1.05` | `0.3024 * (28000 / 15600) * 1.05` |


The target parameters, truncated to 12 fractional places, are:


| Model    | Current coeff (for reference) | `relative_difficulty` | `coeff_min`    | `coeff_max`    |
| -------- | ----------------------------- | --------------------- | -------------- | -------------- |
| MiniMax  | 0.3024                        | 1                     | 0.3024         | 0.3024         |
| GLM      | 0.682                         | 2.9                  | 0.8352        | 1.48176 |
| DeepSeek | 0.2706                        | 1.705882352941        | 0.3825        | 0.569907692307        |




## 4. Targets and controller initialization

Governance chooses `target_share_bps` based on demand, totaling `10000` basis points (100%) across enabled models. MiniMax still needs a target share even though its coefficient is fixed. The equal-share simulation below uses MiniMax `3334`, GLM `3333`, and DeepSeek `3333`.

Targets apply to each model's share of difficulty-normalized PoC weight:

```text
share[i] = D[i] * total_poc_weight[i] / sum_k(D[k] * total_poc_weight[k])
```

The controller adjusts coefficients once per epoch using the previous epoch's shares. It holds a coefficient steady within 5 percentage points of its target. Keep the current settings:

```text
target_zone_bps = 500
step_min = 0.005
step_max = 0.05
bootstrap_step_max = 0.25
bootstrap_share_bps = 100
```

A newly enabled model starts with `base_coeff = coeff_min`, `s = step_max / 2 = 0.025`, and `prev_sign = 0`. Here `s` is the fractional adjustment step, and `prev_sign` records the previous adjustment direction.

When governance changes bounds, difficulties, or targets, existing models carry their previous controller state forward. The next calculation keeps the base coefficient within the new bounds, then applies the normal epoch adjustment.

For epoch 424, all adaptive steps are `0.05`, with previous signs MiniMax `-1`, GLM `+1`, and DeepSeek `+1`. Applying the proposed bounds first clamps GLM from `0.682` to `0.8352` and DeepSeek from `0.2706` to `0.3825`. The normal epoch adjustment follows.

Configuration is frozen at PoC start. When a model exceeds its target share, the excess earns `coeff_min`, reducing the effective coefficient used for participant weights. A zero target pins that model's coefficient to its floor and resets the step/sign state.

See [the protocol](dynamic-coeff.md) for epoch adjustments and [the implementation](dynamic-coeff-impl.md) for state handling.

---



## 5. Simulation

We simulate 50 epochs with the epoch-424 hardware and the parameters above. Targets are 33.34% MiniMax, 33.33% GLM, and 33.33% DeepSeek. Epoch 0 is the starting allocation with the current coefficients. Epoch 1 applies the first coefficient adjustment. The simulation uses the target bounds from section 3 and carries the current coefficients and controller state from sections 1 and 4.

### Hardware and host behavior

The simulation includes 700 reported GPUs across 174 existing nodes owned by 25 hosts.


| GPU family | GPUs |
| --- | ---: |
| H100 | 300 |
| H200 | 197 |
| B200 | 59 |
| B300 | 144 |


The snapshot contains 856 GPUs with positive PoC weight. The 156 GPUs without benchmarks are excluded: 80 H20, 40 A100, 16 RTX PRO 6000, 16 RTX 5090, and 4 H800. H100 PCIe uses the H100 measurements, H200 NVL uses H200 measurements, and both B300 variants use B300 measurements.

- Hardware counts, ownership, and node sizes stay fixed. Initial assignments come from the epoch-424 snapshot. Nodes with zero PoC weight are excluded.
- Each node represents normalized compute capacity. Its rate is `node_GPUs / 8 * q[i,g]`. Every modeled node can switch to any model. Reported counts do not establish physical topology or tensor-parallel deployment limits.
- Hosts maximize their share of a common PoC reward pool: `GNK_host = reward_pool * host_weight / network_weight`.
- Hosts evaluate one node switch at a time. Each candidate is evaluated after recomputing dilution and the weights of all nodes owned by that host.
- A switch must increase host rewards by more than 1% of the switching node's current reward. This threshold represents redeployment cost.
- Every host responds each epoch. Nodes are visited in shuffled order with seed `0` until no profitable single-node switch remains. Coordinated switches of several nodes are outside this model.
- Rewards are proportional to PoC weight. Group caps, collateral adjustments, inference revenue, and redeployment downtime are excluded.



Source: [saved node assignments and hardware](data/epoch424/nodes.csv).

### Coefficients and compute shares

Each coefficient cell shows `base / effective`. The effective coefficient includes dilution and determines rewards. Shares use difficulty-normalized PoC throughput. The starting shares are calculated from the same benchmarks. They are modeled values rather than observed chain shares. The tables show selected simulated epochs, including the stable allocation.


| Epoch | MiniMax coeff | GLM coeff | DeepSeek coeff | MiniMax share | GLM share | DeepSeek share |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 | 0.302400 / 0.302400 | 0.682000 / 0.682000 | 0.270600 / 0.270600 | 62.83% | 12.46% | 24.72% |
| 1 | 0.302400 / 0.302400 | 0.876960 / 0.876960 | 0.401625 / 0.401625 | 81.76% | 0.00% | 18.24% |
| 5 | 0.302400 / 0.302400 | 0.964656 / 0.956281 | 0.400371 / 0.400371 | 64.36% | 35.64% | 0.00% |
| 10 | 0.302400 / 0.302400 | 0.964656 / 0.964656 | 0.419076 / 0.406714 | 17.99% | 31.66% | 50.35% |
| 20 | 0.302400 / 0.302400 | 0.964656 / 0.956281 | 0.385748 / 0.385748 | 64.36% | 35.64% | 0.00% |
| 30 | 0.302400 / 0.302400 | 0.964656 / 0.964656 | 0.412572 / 0.405567 | 24.34% | 32.21% | 43.45% |
| 38 | 0.302400 / 0.302400 | 0.964656 / 0.964656 | 0.411666 / 0.405621 | 25.64% | 32.32% | 42.04% |
| 39-50 | 0.302400 / 0.302400 | 0.964656 / 0.964656 | 0.401374 / 0.399319 | 29.91% | 32.68% | 37.40% |




### GPUs assigned to each model

GPU types are reported by participants and are not independently verified. Different cards can produce equivalent PoC throughput, so measured compute power does not establish the exact GPU type.

Each cell lists individual GPU counts assigned to that model. Omitted GPU types have zero allocation. Epoch ranges share the same allocation.

| Epoch | MiniMax | GLM | DeepSeek |
| --- | --- | --- | --- |
| 0 | 196 H100, 185 H200, 51 B200, 60 B300 | 12 H200, 8 B200, 39 B300 | 104 H100, 45 B300 |
| 1 | 300 H100, 197 H200, 59 B200, 99 B300 | None | 45 B300 |
| 5 | 59 B200, 144 B300 | 300 H100, 197 H200 | None |
| 10 | 59 B200, 12 B300 | 300 H100, 197 H200 | 132 B300 |
| 20 | 59 B200, 144 B300 | 300 H100, 197 H200 | None |
| 30 | 59 B200, 32 B300 | 300 H100, 197 H200 | 112 B300 |
| 38 | 59 B200, 36 B300 | 300 H100, 197 H200 | 108 B300 |
| 39-50 | 59 B200, 49 B300 | 300 H100, 197 H200 | 95 B300 |

### GNK per 8 GPUs relative to 8xH100

This compares the average gross reward per 8 GPUs in each family with the average reward per 8 H100 GPUs in the same epoch. It uses the assignments above, including cases where a GPU family serves several models.

```text
weight_per_8[g] = 8 * sum(weight of nodes in family g) / GPU_count[g]
GNK_ratio[g] = weight_per_8[g] / weight_per_8[H100]
```

H100 is the reference and always has a value of `1.000`. For example, B300 has a value of `5.309` in epochs 39-50. If 8 H100 GPUs earn 100 GNK in one of those epochs, 8 B300 GPUs earn 530.9 GNK on average. The 100 GNK is an example amount.


Start uses the epoch-424 assignments and effective coefficients, calculated with the same benchmarks and reward model.

| GPU | Start | Epoch 1 | Epoch 5 | Epoch 10 | Epoch 20 | Epoch 30 | Epoch 38 | Epochs 39-50 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| H100 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 |
| H200 | 2.110 | 1.793 | 1.650 | 1.650 | 1.650 | 1.650 | 1.650 | 1.650 |
| B200 | 5.375 | 4.828 | 4.427 | 4.389 | 4.427 | 4.389 | 4.389 | 4.389 |
| B300 | 5.562 | 5.862 | 5.376 | 5.391 | 5.376 | 5.370 | 5.369 | 5.309 |


All three shares enter the target zone at simulated epoch 39: MiniMax 29.91%, GLM 32.68%, and DeepSeek 37.40%. Allocations and coefficients stay unchanged through epoch 50, and an extended run confirms the same outcome through epoch 100. The final shares differ from one third because adjustment stops within 5 percentage points of each target.

Reproduce with `python3 proposals/multi-model-poc/simulation/epoch424.py`. [Full results](simulation/epoch424-results.json) include all 50 epochs, GPU allocations, controller state, and switching counts.

Use `--epochs 100 --output /tmp/epoch424-100.json` to repeat the longer stability check.
