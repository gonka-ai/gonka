# Target configuration for dynamic PoC coefficients

Dynamic PoC coefficients were introduced in the [v0.2.16 upgrade](../governance-artifacts/update-v0.2.16/README.md#dynamic-coefficients-v1) with ranges limited to 10% above or below the existing static coefficients.

This proposal widens the ranges for GLM and DeepSeek so coefficient adjustments can incentivize deployment toward the existing target shares of 33.34% MiniMax, 33.33% GLM, and 33.33% DeepSeek. The new upper bounds and relative difficulties use measured PoC throughput from MLNode v4.0.0 on H100, H200, B200, and B300.

> Lower bounds remain unchanged so coefficients can adjust from their current values without a forced increase at activation. The wider ranges strengthen deployment incentives when a model needs more compute and reduce them when it has excess supply.

This document defines the target coefficient bounds and relative difficulties, and simulates applying them from the current first-phase state.

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

Each value below is the number of nonces generated per minute by 8 GPUs of the specified class, using measurements from MLNode v4.0.0.


| Model / variant         | H100 | H200 | B200  | B300  |
| ----------------------- | ---- | ---- | ----- | ----- |
| MiniMax M2.7 FP8        | 5800 | 10400 | 28000 | 34000 |
| GLM 5.3 Flash           | 2000 | 3300 | 6000  | 9200  |
| DeepSeek V4 Flash       | 3400 | 7200 | 15600 | 25600 |


For each hardware class, use the fastest supported variant. For DeepSeek, this selects FP8 on H100/H200 and NVFP4 on B200/B300. The calculation assumes both variants can serve the same chain model.

Source: [MLNode v4.0.0 prefill measurements](data/mlnode-4.0.0-prefill.csv), reported as nonces/min per 8 GPUs.

## 3. Exact formulas

Let `q[i,g]` be model `i` throughput per 8 GPUs of class `g`. Let `b` be MiniMax and `c_b = 0.3024` its fixed coefficient. Let `delta = 0.05` be the desired 5% switching incentive. Take minima and maxima over `g = {H100, H200, B200, B300}`.

The parity coefficient gives a hardware class equal weight on model `i` and MiniMax. Calculate benchmark bounds around the extreme parity values, then widen them to include the existing allowed range. Let `current_min[i]` and `current_max[i]` be the current on-chain bounds.

```text
parity[i,g] = c_b * q[b,g] / q[i,g]

benchmark_min[i] = min_g(parity[i,g]) / (1 + delta)
benchmark_max[i] = max_g(parity[i,g]) * (1 + delta)

coeff_min[i] = min(current_min[i], benchmark_min[i])
coeff_max[i] = max(current_max[i], benchmark_max[i])

D[i] = q[b,H100] / q[i,H100]

coeff_min[b] = coeff_max[b] = c_b
D[b] = 1
```

At parity, `q[i,g] * parity[i,g] = q[b,g] * c_b`. At the floor, MiniMax earns at least 5% more PoC weight before dilution on every measured class. The benchmark ceiling gives model `i` at least 5% more on every measured class before dilution, subject to decimal truncation.

`D` converts each model's PoC weight to comparable compute units using an 8xH100 reference server. It depends only on measured throughput ratios.

For GLM, H100 determines the benchmark lower bound. For DeepSeek, B300 determines the benchmark lower bound. B200 determines both benchmark upper bounds.


| Model    | `D`           | `benchmark_min`                   | `benchmark_max`                 |
| -------- | ------------- | --------------------------------- | ------------------------------- |
| GLM      | `5800 / 2000` | `0.3024 * (5800 / 2000) / 1.05`  | `0.3024 * (28000 / 6000) * 1.05` |
| DeepSeek | `5800 / 3400` | `0.3024 * (34000 / 25600) / 1.05` | `0.3024 * (28000 / 15600) * 1.05` |


The benchmark lower bounds are `0.8352` for GLM and `0.3825` for DeepSeek. Both exceed the current lower bounds, so the proposal retains `0.558` and `0.2214`. The target parameters, truncated to 12 fractional places, are:


| Model    | Current coeff (for reference) | `relative_difficulty` | `coeff_min`    | `coeff_max`    |
| -------- | ----------------------------- | --------------------- | -------------- | -------------- |
| MiniMax  | 0.3024                        | 1                     | 0.3024         | 0.3024         |
| GLM      | 0.682                         | 2.9                  | 0.558        | 1.48176 |
| DeepSeek | 0.2706                        | 1.705882352941        | 0.2214        | 0.569907692307        |




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

For epoch 424, all adaptive steps are `0.05`, with previous signs MiniMax `-1`, GLM `+1`, and DeepSeek `+1`. The widened ranges contain the current coefficients, so no initial clamp is needed. The first simulated adjustment increases GLM from `0.682` to `0.7161` and DeepSeek from `0.2706` to `0.28413`, both by 5%.

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
- Hosts maximize their share of total network weight, `host_weight / network_weight`.
- Hosts evaluate one node switch at a time. Each candidate is evaluated after recomputing dilution and the weights of all nodes owned by that host.
- A switch must increase the host's share of total network weight by more than 1% of the switching node's current share. This threshold represents redeployment cost.
- Every host responds each epoch. Nodes are visited in shuffled order with seed `0` until no profitable single-node switch remains. Coordinated switches of several nodes are outside this model.
- Group caps, collateral adjustments, inference revenue, and redeployment downtime are excluded.



Source: [saved node assignments and hardware](data/epoch424/nodes.csv).

### Coefficients and compute shares

Each coefficient cell shows `base / effective`. The effective coefficient includes dilution and determines participant weight. Shares use difficulty-normalized PoC throughput. The starting shares are calculated from the same benchmarks. They are modeled values rather than observed chain shares. The tables show selected simulated epochs, including the stable allocation.


| Epoch | MiniMax coeff | GLM coeff | DeepSeek coeff | MiniMax share | GLM share | DeepSeek share |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 | 0.302400 / 0.302400 | 0.682000 / 0.682000 | 0.270600 / 0.270600 | 62.83% | 12.46% | 24.72% |
| 1 | 0.302400 / 0.302400 | 0.716100 / 0.716100 | 0.284130 / 0.284130 | 100.00% | 0.00% | 0.00% |
| 2 | 0.302400 / 0.302400 | 0.787710 / 0.787710 | 0.312543 / 0.312543 | 100.00% | 0.00% | 0.00% |
| 3 | 0.302400 / 0.302400 | 0.945252 / 0.945252 | 0.375052 / 0.375052 | 83.17% | 16.83% | 0.00% |
| 4 | 0.302400 / 0.302400 | 0.992515 / 0.992515 | 0.468814 / 0.409663 | 24.02% | 32.18% | 43.80% |
| 5 | 0.302400 / 0.302400 | 0.992515 / 0.992515 | 0.445374 / 0.410111 | 27.93% | 32.51% | 39.56% |
| 6-50 | 0.302400 / 0.302400 | 0.992515 / 0.992515 | 0.423105 / 0.408388 | 31.25% | 32.80% | 35.95% |




### GPUs assigned to each model

GPU types are reported by participants and are not independently verified. Different cards can produce equivalent PoC throughput, so measured compute power does not establish the exact GPU type.

Each cell lists individual GPU counts assigned to that model. Omitted GPU types have zero allocation. Epoch ranges share the same allocation.

| Epoch | MiniMax | GLM | DeepSeek |
| --- | --- | --- | --- |
| 0 | 196 H100, 185 H200, 51 B200, 60 B300 | 12 H200, 8 B200, 39 B300 | 104 H100, 45 B300 |
| 1 | 300 H100, 197 H200, 59 B200, 144 B300 | None | None |
| 2 | 300 H100, 197 H200, 59 B200, 144 B300 | None | None |
| 3 | 197 H200, 59 B200, 144 B300 | 300 H100 | None |
| 4 | 59 B200, 31 B300 | 300 H100, 197 H200 | 113 B300 |
| 5 | 59 B200, 43 B300 | 300 H100, 197 H200 | 101 B300 |
| 6-50 | 59 B200, 53 B300 | 300 H100, 197 H200 | 91 B300 |

### Consensus weight per 8 GPUs

This compares coefficient-adjusted PoC weight per 8 GPUs in each family with 8 H100 GPUs in the same epoch, before collateral adjustments and group caps. It uses the assignments above, including cases where a GPU family serves several models.

```text
weight_per_8[g] = 8 * sum(weight of nodes in family g) / GPU_count[g]
weight_ratio[g] = weight_per_8[g] / weight_per_8[H100]
```

H100 is the reference and always has a value of `1.000`. For example, B300 has a value of `5.235` in epochs 6-50. This means 8 B300 GPUs contribute 5.235 times the coefficient-adjusted PoC weight of 8 H100 GPUs on average.


Start uses the epoch-424 assignments and effective coefficients, calculated with the same benchmarks and weight model.

| GPU | Start | Epoch 1 | Epoch 2 | Epoch 3 | Epoch 4 | Epoch 5 | Epochs 6-50 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| H100 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 |
| H200 | 2.110 | 1.793 | 1.793 | 1.664 | 1.650 | 1.650 | 1.650 |
| B200 | 5.375 | 4.828 | 4.828 | 4.479 | 4.266 | 4.266 | 4.266 |
| B300 | 5.562 | 5.862 | 5.862 | 5.439 | 5.261 | 5.256 | 5.235 |


All three shares enter the target zone at simulated epoch 6: MiniMax 31.25%, GLM 32.80%, and DeepSeek 35.95%. Allocations and coefficients stay unchanged through epoch 50, and an extended run confirms the same outcome through epoch 100. The final shares differ from one third because adjustment stops within 5 percentage points of each target.

Reproduce with `python3 proposals/multi-model-poc/simulation/epoch424.py`. [Full results](simulation/epoch424-results.json) include all 50 epochs, GPU allocations, controller state, and switching counts.

Use `--epochs 100 --output /tmp/epoch424-100.json` to repeat the longer stability check.
