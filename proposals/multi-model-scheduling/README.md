# GIP-XX: Multi-Model Scheduling

| Field | Value |
|---|---|
| **Status** | Draft |
| **Type** | Standards Track (Core) |
| **Author** | 0xgonka |
| **Created** | 2026-05-05 |
| **Requires** | [Multi-Model PoC](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md) |
| **Replaces** | None |

## Abstract

This GIP introduces a two-layer mechanism for allocating compute and pricing
PoC weight across the multiple models supported by the Gonka network.

A new on-chain **GPU class registry** establishes a canonical taxonomy
(replacing today's free-form `Hardware.Type` string). Operators declare a
per-MLNode **capability** that names a canonical GPU class and the set of
models the node has weights for and is willing to serve. Layer 1 is a
deterministic on-chain **default scheduler** that publishes a per-epoch
**opinion**: a target share of compute per `(GPU class, model)` pair. Each
DAPI computes its MLNode assignments as the intersection of the operator's
capability with the chain's opinion, weighted by a **hardware fit factor**
that prefers matching high-end GPUs to high-end models and discounts
wasteful over-provisioning. **Model switches are aligned to epoch
boundaries** so the existing PoC and validation pipelines remain
well-defined under reassignment.

Layer 2 makes `PoCModelConfig.weight_scale_factor` **adaptive within a
governance-set band**, scaling with realized inference demand and modulated
by a **learning-period uncertainty mechanism** for newly approved models.
Auto-deprecation removes models that sustain near-zero demand.

**Per-host quality routing** — biasing which host serves a given inference
request based on that host's historical performance on that model
(latency, error rate, validation pass rate, etc.) — is explicitly out of
scope. This GIP changes *what each model is worth* and *how default supply
is distributed across models*; per-host quality is a separate concern
(*which host should serve a given request*) and is deferred to a follow-on
proposal.

## Motivation

Multi-model PoC shipped with a single governance-set scalar per model
([`PoCModelConfig.weight_scale_factor`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/params.proto#L140-L154))
as the only lever controlling how a model's PoC weight converts to consensus
weight. Aggregation is currently
`Σ weight_scale_factor_i × pocWeight_i` (see
[`weight.go`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/x/inference/types/weight.go)).
This design has three predictable failure modes once the network supports
more than two or three models:

1. **Concentration.** Hosts pile into the highest-paid model. The network
   collapses to an effective single-model state, defeating the multi-model
   architecture.

2. **Cold start under uncertainty.** Newly approved models have no demand
   history, no peer hosts for PoC validation, and an unknown true value.
   Hosts will not voluntarily supply them. New models cannot bootstrap.

3. **Stale tail.** Models with no demand continue to be paid at their
   governance-set rate forever. The chain has no mechanism to retire a
   model without an explicit per-model governance vote.

A fourth concern is operational: **hardware mismatch**. A naive scheduler
that routes by demand alone will assign high-demand small models to
flagship GPUs purely because the demand signal is high, wasting hardware
capacity that small GPUs could serve as efficiently.

A naive composite multiplier on the coefficient does not address (1)
because it requires hosts to *act* on the new signal. The majority of the
network's compute belongs to operators who configure their nodes once and
rarely revisit. Equilibrium must be produced *by default*, not by host
response. And that equilibrium must respect the physical reality that an
operator can only run models they have weights for, and that high-end
hardware should not be assigned wasteful work when better-fit work exists.

This GIP separates the equilibrium-by-default mechanism (the scheduler)
from the price-discovery mechanism (the adaptive coefficient), and grounds
both in operator-declared capability sets and a hardware fit factor.

## Specification

### 1. Architecture

Two cooperating layers, plus a model-lifecycle state machine, all built on
explicit operator-declared capability sets.

- **Capability:** each MLNode declares the set of models it can and will
  serve, persisted on-chain.
- **Layer 1 (Scheduler):** chain-side computation that publishes an
  *opinion* — a target distribution of lazy compute per `(GPU class,
  model)` pair each epoch — weighted by demand and by a hardware fit
  factor. Each DAPI selects its MLNode assignments from the intersection
  of operator capability and the chain's opinion.
- **Layer 2 (Coefficient):** chain-side computation that produces an
  effective per-model `weight_scale_factor` from a governance-set base, an
  observed demand factor, a novelty factor, and a status factor, clamped
  to a governance-set band.
- **Lifecycle:** each model carries a `status` of `LEARNING`, `ACTIVE`,
  `DEPRECATED`, or `RETIRED`. Status transitions are driven by observed
  demand and time, with a governance veto window before terminal
  transitions take effect.

The scheduler is **advisory**. It expresses an opinion over each
operator's declared capabilities; it does not direct hosts to run models
they have not declared themselves capable of running.

Both layers run at epoch settlement. Outputs are persisted on-chain as
part of `EpochGroupData` or analogous per-epoch state and become inputs to
the next epoch.

### 2. New on-chain state

#### 2.1. `EpochModelDemandSummary`

A new collection keyed by `(epoch_index, model_id)` populated at epoch
settlement.

```proto
message EpochModelDemandSummary {
  uint64 epoch_index = 1;
  string model_id = 2;
  uint64 inference_count = 3;       // settled, valid inferences only
  uint64 prompt_token_total = 4;
  uint64 completion_token_total = 5;
  string paid_value_ngonka = 6;     // Dec-encoded; primary demand metric
}
```

`paid_value_ngonka` is the primary demand metric used by both layers. It
MUST aggregate only inferences that settled with status `valid` and that
were paid (not refunded or rejected).

#### 2.2. `PoCModelConfig` extensions

Add the following fields to
[`PoCModelConfig`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/params.proto#L138):

```proto
message PoCModelConfig {
  // existing fields preserved...

  Decimal min_band                  = 10;  // floor for effective coefficient
  Decimal max_band                  = 11;  // ceiling for effective coefficient
  ModelStatus status                = 12;  // LEARNING | ACTIVE | DEPRECATED | RETIRED
  uint64 approved_at_epoch          = 13;  // set when added; immutable
  uint64 status_changed_at_epoch    = 14;  // updated on each status transition
  Decimal scheduler_allocation_cap  = 15;  // 1.0 for ACTIVE, < 1.0 for LEARNING
  string target_gpu_class           = 16;  // GpuClass.id this model was designed for
  repeated string compatible_gpu_classes = 17; // GpuClass.ids that can physically serve
  uint32 min_gpu_count              = 18;  // minimum GPU count of target class
}

enum ModelStatus {
  MODEL_STATUS_UNSPECIFIED = 0;
  MODEL_STATUS_LEARNING    = 1;
  MODEL_STATUS_ACTIVE      = 2;
  MODEL_STATUS_DEPRECATED  = 3;
  MODEL_STATUS_RETIRED     = 4;
}
```

`min_band` and `max_band` MUST satisfy `0 < min_band ≤ base ≤ max_band`.
`target_gpu_class` MUST be an element of `compatible_gpu_classes` and
MUST reference a registered class in §2.6. `min_gpu_count` is the
minimum number of GPUs of the target class required to host one
instance (e.g. flagship 235B FP8 needs ~4× H100/H200 SXM with NVLink).

The existing [`Model.v_ram`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/model.proto)
field is currently stored at registration but never read. It SHOULD be
preserved for informational purposes but is not a substitute for the
explicit `target_gpu_class` declaration: serving a flagship model needs
not just enough VRAM but specific interconnect (NVLink) and precision
(FP8) characteristics that pure VRAM cannot capture.

##### Initial backfill (informed by current network state)

Mainnet currently serves `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`. The
genesis model list also includes `Qwen/QwQ-32B` and
`Qwen/Qwen2.5-7B-Instruct`. Suggested initial values:

| Model | `target_gpu_class` | `compatible_gpu_classes` | `min_gpu_count` |
|---|---|---|---:|
| `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` | `H100_SXM` | `H100_SXM, H200_SXM, B200_SXM` | 4 |
| `Qwen/QwQ-32B` (FP8) | `A100_80` | `A100_80, H100_PCIE, H100_SXM, H200_SXM, L40S` | 1 |
| `Qwen/Qwen2.5-7B-Instruct` (FP8) | `L40S` | `L40S, A100_40, A100_80, H100_PCIE, H100_SXM, H200_SXM` | 1 |

These are starting points; final values to be confirmed by governance
at upgrade time.

#### 2.3. `PocParams` extensions

A new global `fit_decay` parameter governs the hardware fit factor (see
§3.3):

```proto
message PocParams {
  // existing fields preserved...
  Decimal fit_decay = N;  // per-class penalty for over-provisioning; default 0.4
}
```

#### 2.4. `EpochSchedulerTarget`

A new collection keyed by `(epoch_index, gpu_class, model_id)` published
at epoch settlement and read by DAPIs.

```proto
message EpochSchedulerTarget {
  uint64 epoch_index = 1;
  string gpu_class   = 2;
  string model_id    = 3;
  Decimal lazy_share = 4;   // fraction of lazy compute for this (class, model)
}
```

For each `gpu_class`, `Σ lazy_share over all models = 1.0` (or 0 if no
eligible models for that class).

#### 2.5. `MLNodeCapability`

An explicit per-MLNode capability declaration submitted by the operator
and persisted on-chain via a new `MsgSetMLNodeCapability`. It carries
both the canonical GPU class of the MLNode (see §2.6) and the set of
models the operator has weights for and is willing to serve on it.

```proto
message MLNodeCapability {
  string mlnode_id                  = 1;
  string gpu_class                  = 2;  // canonical class id from registry
  repeated string supported_models  = 3;  // models this MLNode is willing/able to serve
  uint64 set_at_epoch               = 4;
}
```

A capability set of size 1 is equivalent to a hard pin. A capability set
of size > 1 indicates the operator is willing to let the scheduler choose
among the listed models (weighted by the chain's opinion). An empty
capability set or unset `gpu_class` (default for newly registered MLNodes
that have not yet declared) means the MLNode is **not eligible for
scheduler assignment** and serves nothing under this GIP's mechanism
until the operator declares.

`gpu_class` is the class of GPUs **allocated to that specific MLNode**,
not necessarily the host's full inventory. A host with both A100_80 and
T4 hardware that runs one vLLM instance on the A100s and another on the
T4s declares two MLNodes with different `gpu_class` values.

`supported_models` MUST contain only models present in the on-chain Model
registry. Declarations referencing unknown or `RETIRED` models MUST be
rejected at message handling. `gpu_class` MUST be a registered class in
the GPU class registry (§2.6).

`MsgSetMLNodeCapability` is a regular paid transaction (not a network
duty) and is subject to standard fees. This is intentional: it provides
a small economic friction against spammed re-declarations that perturb
the scheduler's opinion across epochs (see Security Considerations).

#### 2.6. GPU class registry

A new on-chain collection of canonical GPU classes, governance-maintained.
This is necessary because the existing
[`HardwareNode.hardware[].type`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/hardware_node.proto#L33-L36)
field is a free-form string with no validation, no normalization, and is
not consumed by any production code today. Operator submissions in
practice are coarse strings like `"A100"` or `"T4"` with no variant
information (memory size, NVLink presence, etc.), which is insufficient
for fit-based scheduling.

```proto
message GpuClass {
  string id          = 1;  // canonical identifier, e.g. "H100_SXM"
  uint32 vram_gb     = 2;  // per-GPU VRAM capacity
  bool   nvlink      = 3;  // NVLink interconnect present
  bool   fp8_native  = 4;  // hardware FP8 support
  uint32 tier_rank   = 5;  // position in the global ordering (0 = lowest tier)
}
```

The registry is queried as an ordered set keyed by `tier_rank`. Adding a
new class, removing a class, or reordering existing classes requires a
governance proposal. `tier_rank` values define the ordering used by the
fit factor (§3.3); higher rank = higher tier.

##### 2.6.1. Initial registry contents

Genesis seeding informed by hardware actually in use on the network
today and the model classes the network is expected to serve. Final
values to be confirmed by governance at upgrade time.

| `id` | `vram_gb` | `nvlink` | `fp8_native` | `tier_rank` |
|---|---:|:---:|:---:|---:|
| `T4` | 16 | no | no | 0 |
| `RTX_4090` | 24 | no | no | 1 |
| `L40S` | 48 | no | yes | 2 |
| `A100_40` | 40 | yes | no | 3 |
| `A100_80` | 80 | yes | no | 4 |
| `H100_PCIE` | 80 | partial | yes | 5 |
| `H100_SXM` | 80 | yes | yes | 6 |
| `H200_SXM` | 141 | yes | yes | 7 |
| `B200_SXM` | 192 | yes | yes | 8 |

This list is intentionally narrow at launch (covers what's observed in
testnet fixtures plus the obvious flagship classes). Consumer-grade and
non-Nvidia accelerators can be added by governance as they enter the
network.

##### 2.6.2. Relationship to legacy `Hardware.Type`

The legacy free-form `HardwareNode.hardware[].type` field is preserved
as informational only. It is not used by the scheduler. Operators MUST
declare a canonical `gpu_class` in `MLNodeCapability` for an MLNode to
be eligible for scheduler assignment. A future GIP MAY deprecate the
legacy field once adoption is universal.

### 3. Layer 1: Default scheduler

#### 3.1. Inputs

At settlement of epoch `E`, the scheduler MUST consume:

- The set of all MLNodes registered on the network whose
  `MLNodeCapability` is declared (non-empty `gpu_class` and
  `supported_models`).
- For each model: `status`, `scheduler_allocation_cap`, `target_gpu_class`,
  `compatible_gpu_classes`.
- `EpochModelDemandSummary` records for the trailing window of `D` epochs
  ending at `E` (default `D = 7`).
- The global `fit_decay` parameter and the GPU class registry (§2.6).

#### 3.2. Eligible compute pool

For scheduler purposes, the eligible pool for `(gpu_class c, model m)` is
the set of MLNodes that:
1. Have GPU class `c`.
2. Have a non-empty `MLNodeCapability` containing `m`.
3. Are not pinned to a different model (capability of size 1 referencing
   a different model excludes the MLNode from this pool).

MLNodes with a capability set of size 1 contribute fixed supply to that
one model and are not subject to scheduler reallocation. MLNodes with a
capability set of size > 1 contribute flexible supply that the scheduler's
opinion shapes.

#### 3.3. Hardware fit factor

The scheduler weights its opinion by a fit factor that prefers matching
GPU class to model target and penalizes over-provisioning. Let
`rank(c)` be the `tier_rank` of class `c` in the GPU class registry
(§2.6) — 0 is the lowest tier.

```
fit_factor(c, m) =
    0                                            if c ∉ compatible_gpu_classes(m)
    1.0                                          if rank(c) ≤ rank(target_gpu_class(m))
    fit_decay ^ (rank(c) − rank(target))         if rank(c) >  rank(target_gpu_class(m))
```

Asymmetry is intentional: serving on a class at or below target is fully
weighted (the model is well-matched or efficiently served on smaller
hardware that frees up bigger GPUs for bigger work). Serving on a class
above target is discounted geometrically per class above (over-
provisioning waste).

`fit_decay` defaults to `0.4` (each class above target reduces opinion
weight by ~60%).

#### 3.4. Target distribution

For each `gpu_class c`, let `M(c) = { models m : status(m) ∈ {LEARNING,
ACTIVE} ∧ fit_factor(c, m) > 0 }`. For each `m ∈ M(c)`:

```
weight(c, m) = paid_value_trailing(m, D) × fit_factor(c, m)
                                        × scheduler_allocation_cap(m)
```

If `Σ weight(c, m) over M(c) == 0` (no demand history for any compatible
model), distribute uniformly weighted by fit factor:
`lazy_share(c, m) = fit_factor(c, m) / Σ fit_factor(c, m')`. Otherwise:

```
lazy_share(c, m) = weight(c, m) / Σ weight(c, m')
```

For `LEARNING` models, `scheduler_allocation_cap(m) < 1.0` (default
`0.1`) limits the scheduler's exposure to unproven models even if their
early demand is high. `DEPRECATED` and `RETIRED` models MUST receive
`lazy_share = 0`.

#### 3.5. Hysteresis

The published `EpochSchedulerTarget` for epoch `E+1` MUST be a smoothed
function of the raw computation in §3.4 and the previous epoch's
published target:

```
published(E+1, c, m) = α × raw(E+1, c, m) + (1 − α) × published(E, c, m)
```

with `α` defaulting to `0.25` (governance-tunable). DAPIs MUST further
apply a **minimum-change threshold** (default `0.05` absolute change in
`lazy_share`) before triggering an MLNode reconfiguration, to avoid
thrashing across epochs.

#### 3.6. DAPI behavior (normative)

A DAPI hosting one or more MLNodes MUST, at each epoch:

1. Read `EpochSchedulerTarget` for the current epoch.
2. For each MLNode `n` of GPU class `c` with capability set
   `S_n = supported_models(n)`:
   a. If `|S_n| == 0`: serve nothing (MLNode is undeclared).
   b. If `|S_n| == 1`: serve the single model in `S_n` (effective pin).
   c. If `|S_n| > 1`: select a target model from `S_n` by sampling in
      proportion to `lazy_share(c, m) for m ∈ S_n`, normalized over
      `S_n`. Tiebreaking and rounding MUST be deterministic
      (lexicographic on model ID, largest-remainders for share-to-count
      conversion when an operator has multiple MLNodes of the same class
      and capability set).
3. If the MLNode's currently loaded model differs from its target, and
   the change in `lazy_share` over the previous epoch exceeds the
   minimum-change threshold, the DAPI SHOULD initiate a model switch
   per §3.7.
4. The chain MUST NOT slash or otherwise penalize a DAPI for diverging
   from the published target. The scheduler is advisory.

#### 3.7. Model switch transition

Model switches are aligned to **epoch boundaries**. The chain operates
on epochs already (PoC happens at epoch transitions, validation cuts
off at fixed positions within the epoch); switching models on the same
boundary keeps every other epoch-keyed mechanism well-defined.

A DAPI that has decided to switch an MLNode from model A to model B at
epoch boundary `E → E+1` MUST:

1. **During epoch `E`:** continue serving model A. New `EpochSchedulerTarget`
   for `E+1` is published at the start of `E`'s settlement window, so the
   DAPI has the full inter-epoch interval to prepare.
2. **At the inference cutoff for epoch `E`:** stop accepting new
   inference requests on this MLNode. Drain in-flight requests.
3. **During the inter-epoch window (between epoch `E` end and `E+1` PoC
   stage start):** mark the MLNode `STOPPED`, unload model A, load model
   B, mark the MLNode ready.
4. **At epoch `E+1`'s PoC stage:** PoC model B (see §3.8). Serve model B
   for the remainder of `E+1`.

The inter-epoch window is bounded but not instantaneous; loading
flagship model weights can take several minutes. Operators with
flagship MLNodes who anticipate frequent reassignment SHOULD size their
storage and bandwidth to support the switch within the window. If the
switch cannot complete in time, the DAPI SHOULD continue serving the
old model and re-attempt the switch at the next epoch boundary.

##### Minimum dwell time

To prevent operational thrashing even at epoch granularity, an MLNode
that has switched models at epoch boundary `E → E+1` MUST NOT be
switched again before epoch `E + 1 + min_dwell` (default `min_dwell = 5`
epochs, governance-tunable). DAPIs enforce this locally; the scheduler's
opinion does not override it. An MLNode in dwell hold continues serving
its current model regardless of opinion drift.

#### 3.8. PoC validation interaction

A multi-model PoC requires the validating MLNode to actually run the
model it is being PoC'd against. Combined with §3.7, this gives an
unambiguous rule:

- **The model an MLNode PoCs at epoch `E+1`'s PoC stage is the model it
  serves at the start of epoch `E+1`.**
- A switch initiated for the `E → E+1` boundary takes effect *before*
  the PoC stage of `E+1` (per the §3.7 timeline). The new model is
  what gets PoC'd.
- A switch that fails to complete in the inter-epoch window leaves the
  MLNode on the old model; it PoCs the old model. The DAPI re-attempts
  the switch for `E+1 → E+2`.

This means a switch costs at most one PoC cycle of revenue at the new
model (for the host whose first PoC there is its first PoC ever). This
is a real cost, but it is bounded and predictable, which is why
`min_dwell` exists.

For `LEARNING` models with too few hosts to form a PoC quorum, the
paired validation-grace GIP applies (see Open Issues §1).

### 4. Layer 2: Adaptive coefficient

#### 4.1. Effective coefficient formula

For each model `m` in epoch `E`:

```
effective_coeff(m, E) = clamp(
    base(m) × demand_factor(m, E) × novelty_factor(m, E) × status_factor(m, E),
    min_band(m),
    max_band(m)
)
```

where:

- `base(m)` is `weight_scale_factor` from `PoCModelConfig`.
- `demand_factor(m, E)`, `novelty_factor(m, E)`, `status_factor(m, E)`
  are defined below.

The result is what consensus-weight aggregation MUST use in place of the
static `weight_scale_factor`. The on-chain
[`ConfirmationWeightScale`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/x/inference/types/weight.go)
record SHOULD be extended to carry the effective coefficient so it is
observable per epoch.

#### 4.2. `demand_factor`

Let `share(m, E)` be model `m`'s share of total network paid inference
value over the trailing window `D`:

```
share(m, E) = paid_value_trailing(m, D) / Σ paid_value_trailing(m', D)
```

Then `demand_factor(m, E)` is a normalized scaling of `share(m, E)`
against a governance-set reference share `s_ref` (default `1 / |active
models|`):

```
demand_factor(m, E) = clamp(share(m, E) / s_ref, 0.5, 2.0)
```

Bounds (`0.5` and `2.0`) are governance-tunable parameters.

#### 4.3. `novelty_factor` and the learning period

A model with `status == LEARNING` has an effective coefficient that
floats freely within `[min_band, max_band]` where `min_band` and
`max_band` are intentionally set wider for learning models than for
active ones. The `novelty_factor` is `1.0` (it does not multiply); the
*width of the band* is what encodes uncertainty.

The learning period lasts `K` epochs (default `K = 30`) from
`approved_at_epoch`. At graduation:
- `status` transitions to `ACTIVE`.
- `min_band` and `max_band` MUST be reset to the standard active range
  (e.g. via the same params governance sets per model).
- `scheduler_allocation_cap` MUST be set to `1.0`.

The graduation transition is automatic at `approved_at_epoch + K` and
requires no governance action.

#### 4.4. `status_factor`

| status | status_factor |
|---|---|
| `LEARNING` | 1.0 |
| `ACTIVE` | 1.0 |
| `DEPRECATED` | linear decay from 1.0 to 0.0 over deprecation window `W_d` (default `10` epochs) |
| `RETIRED` | 0.0 |

### 5. Lifecycle

#### 5.1. State machine

```
        approved
   ────────────────► LEARNING
                       │
                       │ at approved_at_epoch + K
                       ▼
                     ACTIVE ◄────────────────┐
                       │                     │ governance veto
                       │ trailing demand     │ during W_v
                       │ < threshold for     │
                       │ W_q epochs          │
                       ▼                     │
                  DEPRECATED ────► (recovery)─┘
                       │
                       │ status_changed_at_epoch + W_d + W_v
                       │ elapsed without veto
                       ▼
                    RETIRED
```

#### 5.2. Deprecation trigger

At each epoch settlement, for each `ACTIVE` model: if
`paid_value_trailing(m, W_q) / Σ paid_value_trailing(·, W_q) <
deprecation_threshold` (default `0.001`, governance-tunable) and `W_q`
epochs (default `30`) have elapsed since the last status change,
transition `ACTIVE → DEPRECATED`.

#### 5.3. Recovery

A `DEPRECATED` model whose trailing share rises back above
`recovery_threshold` (default `0.005`) before retirement is transitioned
back to `ACTIVE`. Recovery resets the deprecation timer.

#### 5.4. Retirement

A `DEPRECATED` model that remains deprecated for `W_d + W_v` epochs
(default `W_d = 10`, `W_v = 20`) without recovery and without governance
veto transitions to `RETIRED`. `W_v` is the governance veto window during
which a governance proposal MAY override the pending retirement.

A `RETIRED` model is removed from PoC entirely; its
`scheduler_allocation_cap` is `0`, its `effective_coeff` is `0`, and it
is excluded from `EpochModelDemandSummary` aggregation going forward.
`MLNodeCapability` declarations referencing a `RETIRED` model have that
model filtered out at scheduler input time.

An MLNode whose capability set after filtering is empty (i.e. its only
declared model was retired) becomes unschedulable until its operator
submits a new `MsgSetMLNodeCapability` declaring at least one
non-retired model. Until then the MLNode serves nothing under this
GIP's mechanism. The deprecation/retirement timeline (`W_d + W_v`,
default 30 epochs) gives operators ample time to update their
declarations before retirement actually takes effect.

#### 5.5. Governance overrides

Governance MAY at any time:
- Force a status transition between any two states (subject to standard
  governance procedure).
- Override `min_band` / `max_band` per model.
- Adjust the global tuning parameters (`D`, `K`, `W_q`, `W_d`, `W_v`,
  `s_ref`, `α`, `fit_decay`, deprecation/recovery thresholds,
  `demand_factor` bounds).
- Add, remove, or reorder entries in the GPU class registry (§2.6).

### 6. Genesis and migration

At the upgrade introducing this GIP:

1. The GPU class registry (§2.6) MUST be initialized at upgrade with the
   seed contents in §2.6.1, subject to governance refinement.

1a. The existing `MsgRegisterModel` (and its governance-flow analogue
    used to add new models) MUST be extended with `target_gpu_class`,
    `compatible_gpu_classes`, and `min_gpu_count` fields, all required
    for new model registrations going forward.

2. Every existing `PoCModelConfig` MUST be backfilled with:
   - `status = ACTIVE` (existing models skip the learning period).
   - `approved_at_epoch = current_epoch`.
   - `status_changed_at_epoch = current_epoch`.
   - `scheduler_allocation_cap = 1.0`.
   - `min_band = base × 0.5`, `max_band = base × 2.0` (subject to
     governance refinement post-upgrade).
   - `target_gpu_class`, `compatible_gpu_classes`, and `min_gpu_count`
     set per the table in §2.2 for the three currently-known models.
     Models added between this GIP being drafted and shipped MUST be
     assigned values by governance at upgrade time.

3. **MLNode capability declarations are NOT auto-backfilled.** Each
   operator MUST submit `MsgSetMLNodeCapability` for each of their
   MLNodes after the upgrade in order to participate in scheduling.
   Until they do, the MLNode is invisible to the scheduler and continues
   serving whatever it was serving before the upgrade (driven by the
   operator's existing
   [`ENFORCED_MODEL_ID`](https://github.com/gonka-ai/gonka/blob/main/decentralized-api/broker/enforced_model.go)
   configuration). This is intentional: auto-backfilling would require
   the chain to guess the canonical `gpu_class` from the free-form
   legacy `Hardware.Type` string, and there is no reliable mapping.
   Operators know their hardware; they declare it explicitly.

4. `EpochModelDemandSummary` MUST begin populating at the first epoch
   after the upgrade. The trailing-window demand factor reads as `1.0`
   (neutral) until at least `D` epochs of history exist.

5. The first `EpochSchedulerTarget` is published at the epoch boundary
   following the upgrade. Until operators submit
   `MsgSetMLNodeCapability` declarations, the eligible pool is empty
   and no MLNode is reallocated. Reallocation phases in as the
   operator base declares.

## Rationale

### Why two layers?

Conflating equilibrium and pricing into a single composite multiplier
forces every host to be a savvy market participant. In practice most
network compute belongs to operators who configure their nodes once and
rarely revisit. A composite multiplier produces correct behavior only
when hosts respond to it. Splitting the design into a default scheduler
(which allocates without requiring response) and an adaptive coefficient
(which prices for those who do respond) ensures equilibrium even when
most operators are passive.

### Why operator-declared capability sets rather than a directive scheduler?

The chain cannot reliably know which model weights an operator has
downloaded, which models they've configured their inference stack to
serve, or which they're willing to host given off-chain considerations.
A directive scheduler would frequently assign work that a host cannot
physically perform.

Capability sets push that knowledge to the only party that has it (the
operator) and let the chain's scheduler express a useful opinion *over
the operator's stated capabilities*. This collapses the
"recommendation vs. directive" question — the opinion is always
recommendation-shaped because it operates within the operator's declared
domain. It also dissolves switching cost as a protocol concern: a host
only ever switches *within* its declared capability set, where weights
are presumably already on disk and warmable.

### Why an asymmetric hardware fit factor?

A demand-only scheduler will route H100 lazy compute to whichever model
has the most demand, even if that model is a 7B that runs efficiently on
an L40S. The H100 capacity is wasted; the L40S could have served the
same demand and the H100 could have served a flagship model that
actually needs it.

The fit factor encodes "high-end hardware should preferentially serve
high-end work." Asymmetric (penalty for over-provisioning, no penalty
for under-provisioning) because:

- Serving a model on hardware *above* its target wastes capacity.
- Serving a model on hardware *at or below* its target uses capacity
  efficiently — and frees the bigger hardware for bigger work.

The geometric decay (`fit_decay^k` per class above target) means the
penalty is sharp: each class up roughly halves the opinion weight. This
is enough that flagship hardware will overwhelmingly prefer flagship
models when any flagship demand exists, and only fall back to smaller
models when there is no flagship work to do.

### Why an uncertainty band for new models, not a fixed subsidy?

A fixed bootstrap subsidy assumes the chain knows the model's true
value and is paying extra to attract first hosts. The chain does *not*
know the true value of a brand-new model. A wide-band, capped-allocation
learning period is honest about the uncertainty, accumulates the data
needed to price the model correctly, and self-resolves at graduation
without requiring a separate unwind mechanism. It also bounds damage
from a bad new model on both sides (band floor + scheduler cap +
voluntary capability declaration).

### Why settled paid value rather than token count?

Tokens correlate with compute but not with what the network actually
collected. Paid value reflects both volume and price, captures the
demand-side willingness to pay, and is harder to game than token count
(which can be inflated with cheap or self-directed traffic).

### Capability adoption replaces the bootstrap problem

In a directive-scheduler design, bootstrapping a new model means
"convince hosts to switch onto it." In a capability-set design,
bootstrapping a new model means "convince hosts to add it to their
capability sets." That's a meaningfully different (and more honest)
problem:

- The chain cannot allocate compute that no operator has declared
  capability for, regardless of how attractive the coefficient is.
- The wide coefficient band still attracts savvy operators once the
  first few have declared.
- The first declarations need an off-chain or off-protocol push (e.g.
  the model proponent coordinating with hosts, or a future capability-
  declaration bounty mechanism). That push is a coordination problem,
  not a protocol parameter problem.

### The savvy/lazy dynamic is intentional

A savvy operator can ignore the scheduler entirely by declaring a
capability set of size 1 pinned to whichever model has the highest
`effective_coeff`. They capture the high-revenue spot; the lazy
operators (capability set of size > 1) absorb the rebalancing the
scheduler does to keep other models served. This is by design rather
than a flaw:

- The savvy operator is doing exactly what a market participant should
  do — responding to a price signal. The point of the coefficient layer
  is to provide that signal.
- The lazy operator gives up some upside in exchange for not having to
  monitor coefficients themselves. They get the average, the savvy
  operator gets the peak. That tradeoff is the operator's choice.
- The network gets equilibrium because most operators are lazy and the
  scheduler's allocation across them produces balance. A few savvy
  operators arbitraging the margins do not break this — they are a
  small fraction of supply.

This dynamic does mean the scheduler is **only useful when most
operators choose lazy mode**. If every operator declares a capability
set of size 1, the scheduler is a no-op and the network's equilibrium
depends entirely on the coefficient layer plus operator response. That
state is degenerate but not broken; it's just the system without
Layer 1's benefit.

### Why per-host quality routing is out of scope

By "per-host quality routing" we mean: for each `(host, model)` pair,
tracking signals like p95 latency, error/timeout rate, cancel rate, and
validation pass rate, then biasing inference-request routing toward
hosts with a better track record on the requested model. It changes
*who gets the work*, not *what the work is worth*.

This GIP treats all hosts on a given model as interchangeable for
coefficient and scheduler purposes. Per-host quality is a routing-layer
adjustment that is largely independent of the consensus-weight
rebalancing here. The signals it needs are not currently settled chain
state, the gaming surface is materially larger than for the per-model
demand factor, and the natural prototype location is the DAPI's
request-routing logic before any chain-state promotion. Bundling it
would expand scope without improving either system. A follow-on GIP can
specify per-host quality once layers 1 and 2 are operating in production.

## Backwards compatibility

This GIP is a consensus-affecting change and requires a coordinated
upgrade.

- The static `weight_scale_factor` field is preserved as the `base`
  parameter; existing values are not lost.
- Consensus-weight aggregation switches from `weight_scale_factor` to
  `effective_coeff`. With the migration defaults (`demand_factor` reads
  as `1.0` until enough history exists, `status_factor = 1.0` for active
  models, `novelty_factor = 1.0`), the effective coefficient equals the
  base immediately post-upgrade. Behavior is unchanged at the upgrade
  boundary; divergence develops only as demand history accumulates.
- DAPIs that do not implement §3.6 continue functioning with operator-
  configured model assignments. The scheduler is advisory; ignoring it
  is not a protocol violation.
- `MLNodeCapability` is **not** auto-backfilled. Existing MLNodes
  continue serving their `ENFORCED_MODEL_ID` configuration unchanged
  until the operator submits an explicit `MsgSetMLNodeCapability`.
  Auto-backfill would require guessing the canonical `gpu_class` from
  legacy free-form `Hardware.Type` strings (e.g. `"A100"` could mean
  `A100_40` or `A100_80`), which is unreliable. The scheduler is a
  no-op for an MLNode until its operator declares.
- The legacy `HardwareNode.hardware[].type` field is preserved
  unchanged for informational purposes; it is not consumed by this
  GIP's mechanism.

## Security considerations

### Demand metric gaming

The demand factor responds to settled paid value. An attacker
controlling both a payer account and a host could submit self-directed
traffic to inflate a model's demand factor and steal coefficient share
from legitimate models. Mitigations:

- Only `valid`-status, paid, non-refunded inferences count.
- The demand factor is bounded (`clamp(·, 0.5, 2.0)` by default), so
  even a successful inflation attack has a bounded multiplicative effect
  on the coefficient.
- Settled inference fees represent a real cost to the attacker; the
  attack is not free.

A future hardening could require demand attribution to come from
developer/TA accounts above some reputation threshold, or weight demand
by the diversity of paying accounts. Not specified here.

### Scheduler determinism

Layer 1 must produce the same `EpochSchedulerTarget` on every node from
identical on-chain state. Sources of nondeterminism MUST be eliminated:
floating-point arithmetic is forbidden (use `Decimal`); map iteration
MUST be replaced with sorted-key iteration; tiebreaking MUST be explicit
(lexicographic on model ID and MLNode ID).

### GPU class registry integrity

The hardware fit factor depends on the GPU class registry's `tier_rank`
ordering reflecting real hardware tiers. An incorrect or inverted
ordering would route flagship work to small GPUs and small work to
flagship GPUs. Governance MUST audit any change to the registry
carefully, and a change that demotes an existing class SHOULD be
treated as exceptional. The routine operation is **adding a new class
at the correct position** as new GPU generations enter the network.

A second concern: an operator could declare a higher `gpu_class` than
they actually run, capturing a larger share of high-tier opinion than
their hardware deserves. The chain has no way to verify GPU identity
cryptographically. Mitigation: the existing PoC validation system
already produces a hardware-derived `pocWeight` that is independent of
the operator's class declaration. A node that declares `H100_SXM` but
runs a `T4` will produce `T4`-shaped PoC weight, which limits the
absolute consensus weight they can earn. The class declaration affects
*which models the scheduler suggests they run*, not how their PoC
weight is valued. So the abuse surface is "I get assigned flagship work
I can't actually serve well," which the demand factor and per-host
quality routing (future GIP) will eventually penalize via reduced
revenue.

### Capability declaration consistency

`MLNodeCapability` declarations affect which MLNodes appear in which
eligible pools, and therefore the scheduler's published opinion. They
MUST be persisted on-chain so all nodes compute the same target. A
declaration set mid-epoch takes effect at the next
`EpochSchedulerTarget` publication. An operator can spam declaration
changes to perturb the opinion across epochs; this should be discouraged
either via gas cost on `MsgSetMLNodeCapability` or via a minimum
inter-change interval.

### Bounded damage from learning-period models

A newly approved model with `status == LEARNING` is bounded on three
axes:
- Coefficient bounded by `min_band` and `max_band` (wider than active
  but finite).
- Compute exposure bounded by `scheduler_allocation_cap` (default `0.1`
  of capable lazy compute).
- Voluntary capability declaration: no operator is forced to declare
  capability for a learning model; bad models attract no capability
  declarations and self-quarantine.

### Auto-deprecation of useful niche models

A genuinely useful but low-volume model could fall below
`deprecation_threshold` and be auto-deprecated. The veto window `W_v`
(default 20 epochs) gives governance time to intervene before retirement
becomes terminal. Recovery from `DEPRECATED → ACTIVE` is automatic if
demand returns above `recovery_threshold` before retirement.

### Validation interaction (paired GIP)

Learning-period models with very few hosts cannot pass standard PoC
validation alone. This GIP defers the validation grace mechanism to a
paired GIP (see §Open issues). Until that paired GIP ships, the
practical constraint is that a model SHOULD NOT be approved unless at
least the minimum-quorum number of hosts have pre-committed to declaring
capability and running it.

## Phased rollout

The specification above defines the full target system. Implementation
SHOULD ship in three phases on consecutive network upgrades to limit
risk.

**Phase 1.** New on-chain state: GPU class registry (§2.6),
`EpochModelDemandSummary`, `MLNodeCapability`, `EpochSchedulerTarget`,
per-model `target_gpu_class` / `compatible_gpu_classes` / `min_gpu_count`,
global `fit_decay`. New transactions: `MsgSetMLNodeCapability` (operator)
and governance proposals for registry maintenance. Layer 1 scheduler
computation including hardware fit. DAPI consumer that reconfigures
MLNodes within the operator's declared capability. Coefficient remains
static. Largest equilibrium impact, no revenue effect.

**Phase 2.** Layer 2 adaptive coefficient (demand-responsive), with
bands and `demand_factor`. Revenue is now responsive to demand. Bands
kept conservative initially.

**Phase 3.** Lifecycle state machine: learning period, deprecation,
retirement. Requires paired validation-grace GIP for full functionality.

## Open issues

1. **Validation grace for learning-period models** is deferred to a
   paired GIP. Without it, the practical pre-condition for approving a
   new model is that enough hosts have pre-committed capability to
   satisfy PoC validation quorum from epoch one of the learning period.

2. **Capability declaration incentives.** The bootstrap problem for new
   models is now "get hosts to declare capability." This GIP does not
   specify a protocol-level incentive (off-chain coordination is the
   v1 answer). A follow-on may add a small on-chain bounty for early
   capability declarations on newly approved models.

3. **MLNode capability declaration adoption.** The scheduler does
   nothing for an MLNode until its operator submits
   `MsgSetMLNodeCapability` with a canonical `gpu_class` and a
   `supported_models` set. Adoption is voluntary and gradual; the
   network gets the equilibrium benefit only as operators declare. An
   off-protocol coordination effort (operator outreach, documentation,
   migration tooling in the DAPI) is required to drive adoption. A
   future GIP MAY introduce a small capability-declaration bounty if
   adoption stalls.

4. **Default parameter values** (`D`, `K`, `α`, `s_ref`,
   `scheduler_allocation_cap`, `fit_decay`, deprecation/recovery
   thresholds, band widths) given in this spec are starting points.
   Final values SHOULD be set after testnet observation and MAY be
   revised post-deployment by governance.

5. **Per-host quality routing** is acknowledged as a third layer above
   the two specified here and is deferred to a follow-on GIP. Off-chain
   prototyping in the DAPI is encouraged in the interim.

## References

- [Multi-Model PoC proposal](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md)
- [`PoCModelConfig.weight_scale_factor`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/params.proto#L140-L154)
- [`weight.go` consensus-weight aggregation](https://github.com/gonka-ai/gonka/blob/main/inference-chain/x/inference/types/weight.go)
- [`Inference.model` field](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/inference.proto#L37)
- [`EpochPerformanceSummary`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/epoch_performance_summary.proto)
- [`enforced_model.go`](https://github.com/gonka-ai/gonka/blob/main/decentralized-api/broker/enforced_model.go)
