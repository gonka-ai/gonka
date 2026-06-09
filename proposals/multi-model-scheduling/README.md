# GIP-XX: Multi-Model Scheduling

| Field | Value |
|---|---|
| **Status** | Draft |
| **Type** | Standards Track (Core) |
| **Author** | 0xgonka |
| **Created** | 2026-05-05 |
| **Revised** | 2026-06-09 |
| **Requires** | [Multi-Model PoC](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md) |
| **Replaces** | None |

## Abstract

This GIP introduces a two-layer mechanism for allocating compute and pricing
PoC weight across the multiple models supported by the Gonka network. The
mechanism keeps the chain a thin substrate (model registry, demand signal,
adaptive coefficient) and pushes scheduling intelligence to the operator's
DAPI as a local rational agent.

**Layer 1 (Local rational agent).** Each DAPI runs a rational agent that
decides which model its MLNodes will run next epoch by maximizing expected
value: model demand × adaptive coefficient ÷ predicted next-epoch supply,
net of a measured switching-cost discount. Predicted supply incorporates
**switching-intent announcements** (a repurposing of the existing
`MsgDeclarePoCIntent` from multi-model PoC) discounted by a **per-agent
honesty reputation** for each announcer. Operators with no preferences get
sensible defaults from the agent. Operators who want manual control can pin
the agent to a specific model or disable it entirely.

**Layer 2 (Adaptive coefficient).** `PoCModelConfig.weight_scale_factor`
becomes adaptive within a governance-set band, scaling with realized
inference demand. The band is intentionally wider during the **learning
period** of newly approved models (reusing the existing
[multi-model PoC](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md)
`penalty_start_epoch` boundary as the LEARNING/ACTIVE divide, no new state
introduced). Auto-deprecation removes models that sustain near-zero demand,
with a governance veto window before terminal retirement.

This GIP introduces **no on-chain capability registry, no on-chain GPU class
taxonomy, and no chain-published scheduler opinion**. All scheduling
intelligence lives client-side, where the relevant information (hardware,
weights on disk, operator risk tolerance) actually exists. The chain
provides demand and pricing signals; agents react. Equilibrium emerges from
independent rational decisions, not from central direction.

**Per-host quality routing** — biasing which host serves a given inference
request based on that host's historical performance on that model
(latency, error rate, validation pass rate, etc.) — is explicitly out of
scope. This GIP changes *what each model is worth* and *which model each
operator chooses to run*; per-host quality is a separate concern (*which
host should serve a given request*) and is deferred to a follow-on proposal.

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

A naive composite multiplier on the coefficient does not address (1)
because it requires hosts to *act* on the new signal. The majority of the
network's compute belongs to operators who configure their nodes once and
rarely revisit. Equilibrium must be produced *by default*, not by host
response. And that equilibrium must respect the physical reality that an
operator can only run models they have weights for and that switching
between models is costly.

This GIP separates the equilibrium-by-default mechanism (the local rational
agent, which acts on the operator's behalf even when the operator does
nothing) from the price-discovery mechanism (the adaptive coefficient).
Both run with no new on-chain registry overhead.

## Specification

### 1. Architecture

Two cooperating layers, plus end-of-life additions to the existing model
lifecycle.

- **Layer 1 (Local rational agent):** off-chain, runs in the DAPI. Each
  agent observes chain state (current per-model supply, demand history,
  switching-intent announcements, honesty history per participant) and
  decides locally which model each of its MLNodes will run next epoch.
  Default behavior is sensible without operator tuning; operators may
  override by pinning or disabling the agent.
- **Layer 2 (Adaptive coefficient):** chain-side computation that produces
  an effective per-model `weight_scale_factor` from a governance-set base,
  an observed demand factor, and a status factor, clamped to a
  governance-set band.
- **Lifecycle:** the existing
  [multi-model PoC](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md)
  design already encodes the bootstrap → active transition via
  `penalty_start_epoch` per model. This GIP **reuses** that boundary as
  the LEARNING/ACTIVE divide and **adds** the ACTIVE → DEPRECATED →
  RETIRED end-of-life transitions.

The rational agent is **purely client-side**. The chain does not direct,
verify, or enforce its decisions. The chain provides public information
(demand summaries, switching-intent announcements, per-participant
fulfillment history) and the agent reasons over that information locally.
Two different DAPI implementations may make different decisions from
identical chain state; this is acceptable and expected.

Layer 2 runs at epoch settlement. Its output (the effective coefficient
per model per epoch) becomes the consensus-weight multiplier for the next
epoch.

### 1.1. Relationship to existing multi-model PoC

This GIP layers on top of, and does not replace, the participation,
delegation, and PoC mechanisms specified in
[multi-model PoC](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md).
Specifically:

- **Participation modes** (DIRECT, DELEGATE, REFUSE, INTENT, NONE) are
  unchanged. The semantic change in this GIP is that INTENT for an *active*
  model also carries meaning (as a switching announcement consumed by other
  rational agents). See §2.3.
- **`penalty_start_epoch`** per model is reused directly as the
  LEARNING/ACTIVE boundary (see §4.3). No new "learning period" field
  is introduced.
- **Bootstrap pre-eligibility** (`BootstrapDelegationSnapshot`,
  `deploy_window`, INTENT) continues to operate as specified for new
  models. The wider coefficient band during LEARNING (§4.3) is the
  bootstrap subsidy: it makes the coefficient EV attractive enough that a
  rational agent will accept the switching cost to deploy an unproven
  model. The chain does not allocate compute to learning models directly;
  the agent does, by following the coefficient signal.
- **The founding-model weight-cap exemption** (`initial_model_id` =
  Qwen3-235B-FP8) is preserved by this GIP. The exemption applies to the
  legacy weight-cap; this GIP's coefficient bands apply to all models
  including the founding model, with backfill values that produce no
  behavioral change at the upgrade boundary (see §6).

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
were paid (not refunded or rejected). It is consumed both by Layer 2
(adaptive coefficient) and by Layer 1 rational agents (EV computation).

#### 2.2. `PoCModelConfig` extensions

Add the following fields to
[`PoCModelConfig`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/params.proto#L138):

```proto
message PoCModelConfig {
  // existing fields preserved...

  Decimal min_band              = 10;  // floor for effective coefficient
  Decimal max_band              = 11;  // ceiling for effective coefficient
  Decimal min_band_learning     = 12;  // wider floor before penalty_start_epoch
  Decimal max_band_learning     = 13;  // wider ceiling before penalty_start_epoch
  ModelStatus status            = 14;  // ACTIVE | DEPRECATED | RETIRED
  uint64 status_changed_at_epoch = 15; // updated on each status transition
  uint32 min_gpu_count          = 16;  // minimum GPU count this model needs
  uint32 min_vram_gb_total      = 17;  // minimum total VRAM (GB) across GPUs
}

enum ModelStatus {
  MODEL_STATUS_UNSPECIFIED = 0;
  MODEL_STATUS_ACTIVE      = 1;
  MODEL_STATUS_DEPRECATED  = 2;
  MODEL_STATUS_RETIRED     = 3;
}

// LEARNING is not a status; it is derived from
// (current_epoch < penalty_start_epoch) using the existing field
// from PoCModelConfig defined in multi-model PoC. See §4.3.
```

`min_band` and `max_band` MUST satisfy `0 < min_band ≤ base ≤ max_band`.
`min_gpu_count` and `min_vram_gb_total` are advisory hardware floors
consumed by Layer 1 rational agents (§3.1) to compute their local capability
set. The chain does not verify operator hardware against them.

##### Initial backfill (informed by current network state)

Mainnet currently serves `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`. The
genesis model list also includes `Qwen/QwQ-32B` and
`Qwen/Qwen2.5-7B-Instruct`. Suggested initial values:

| Model | `min_gpu_count` | `min_vram_gb_total` |
|---|---:|---:|
| `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` | 4 | 320 |
| `Qwen/QwQ-32B` (FP8) | 1 | 48 |
| `Qwen/Qwen2.5-7B-Instruct` (FP8) | 1 | 24 |

These are starting points; final values to be confirmed by governance at
upgrade time. Note that the hardware floor is informational only — an
operator running an MLNode below these floors will fail PoC naturally
(low throughput, OOM, or both); the chain does not need to enforce it.

#### 2.3. INTENT semantics extension

[`MsgDeclarePoCIntent`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/tx.proto)
is preserved unchanged at the message-type level. The semantic change is
in interpretation:

- **Bootstrap mode (existing).** An INTENT for a model with
  `current_epoch < penalty_start_epoch` (LEARNING) signals the operator's
  intent to deploy that model for bootstrap purposes. The chain consumes
  this for pre-eligibility computation per multi-model PoC.
- **Switching mode (new).** An INTENT for a model with
  `current_epoch >= penalty_start_epoch` (ACTIVE) functions as a
  switching announcement: the operator declares they will run that model
  starting at epoch `submission_epoch + 1`. The chain treats this as
  purely informational — no chain-side reputation, no penalty, no
  enforcement. Rational agents (§3) consume it locally as a market signal.

The submission deadline for a switching-mode INTENT is the
**switching INTENT cutoff**: the last 5% of the epoch's block height
(governance-tunable). INTENT submissions for active models after the
cutoff MUST be rejected at message handling. The cutoff ensures other
agents have time to read announcements and react before the epoch
boundary.

INTENT is **not** in the fee-exempt list (per
[`liquidity_pool_fee_bypass_decorator.go`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/app/ante/) and
similar). This is intentional: switching-mode INTENT is a market signal,
not a protocol duty. The fee provides spam resistance and a small honesty
incentive. Bootstrap-mode INTENT (for LEARNING models) follows whatever
fee policy multi-model PoC specifies; this GIP does not modify it.

### 3. Layer 1: Local rational agent

#### 3.1. Hardware-derived capability set

Each DAPI's rational agent computes its own capability set locally from
information the operator already has: the GPUs available to each MLNode,
which model weights are present on disk, and the chain's
`min_gpu_count` / `min_vram_gb_total` floors per model:

```
capable(node) = { m :
    node.gpu_count >= min_gpu_count(m) AND
    node.total_vram_gb >= min_vram_gb_total(m) AND
    weights_on_disk(node, m) AND
    status(m) != RETIRED
}
```

The chain does NOT need to know `capable(node)`. The agent uses it locally
to filter candidates for the switching decision.

For nodes where weights are not yet on disk but could be downloaded, the
agent MAY include them as "potentially capable" with a higher effective
switching cost (download + warm-up time). Default DAPI behavior is to
only include weights already cached; aggressive operators can configure
the agent to consider downloads.

#### 3.2. Decision criterion

Approaching an epoch boundary, the agent computes expected value (EV) per
candidate model:

```
EV(node, m, next_epoch) =
    pot(m, next_epoch) × throughput(node, m) /
    max(predicted_supply(m, next_epoch), throughput(node, m))
```

where:

- `pot(m, next_epoch) = paid_value_trailing(m, D) × effective_coeff(m, next_epoch)`
  — the value pool for model m next epoch. `D` is the trailing demand
  window (default 7 epochs).
- `throughput(node, m)` is the agent's expected PoC throughput on model
  m, derived locally from the hardware and the model's compute profile.
  For new candidate models without a historical baseline, the agent may
  use a conservative estimate based on the GPU class.
- `predicted_supply(m, next_epoch)` accounts for current supply plus
  announced inflows/outflows (§3.4), each weighted by the announcer's
  reputation (§3.5).
- The `max(·, throughput)` floor in the denominator bounds EV by the full
  pot: if the agent would be alone on model m, supply is exactly its own
  throughput.

The agent decides to switch from `current_model` to `m*` iff:

```
EV(node, m*, next_epoch) >
    EV(node, current_model, next_epoch) × (1 + switch_threshold + switch_cost_fraction(m*))
```

with:

- `switch_threshold` = default 0.05 (5% EV margin required to switch).
  This is the agent's hysteresis. Operators may tune locally — aggressive
  arbitrage at 0.02, conservative stability at 0.10.
- `switch_cost_fraction(m*)` is the agent's estimate of one epoch's
  earnings lost to downtime when switching to m*. See §3.3.

When no candidate satisfies the threshold, the agent stays on
`current_model`. Including the switching cost in the EV calculation gives
natural hysteresis: switching only happens when expected gains exceed both
the threshold AND the realized cost.

#### 3.3. Switching cost estimation

The agent maintains a per-model estimate of switching downtime, derived
empirically from public chain data. No chain-side state is required.

**Observation source.** Each PoC commit on the chain carries the
participant's model ID. The agent watches the chain across epoch
boundaries and identifies transitions: the gap (in blocks) between the
last PoC commit for participant `p` on model `A` and the first PoC commit
for the same participant on model `B` is a noisy estimate of `p`'s
switching cost into `B`.

**Aggregation.** Per target model `m`, the agent maintains a rolling
sample of the last N observed transitions into `m` from any participant.
Suggested defaults:

- Sample size: last 50 observations (per target model).
- Statistic: median, not mean. Robust to outliers (the operator who took
  three hours to switch because their cluster was down should not skew
  the population estimate).
- Outlier exclusion: any observation > 2 hours is discarded as an ops
  failure, not switching cost.

**Priors.** Until N ≥ 10 observations have accumulated, the agent uses a
conservative prior:

- Default prior for models with `min_gpu_count <= 2`: 30 minutes.
- Default prior for models with `min_gpu_count > 2` (flagship): 45 minutes.

The conservative prior errs toward overestimating cost, which biases the
agent toward stability rather than oscillation when its empirical
knowledge is thin.

**Conversion to `switch_cost_fraction`.** Divide the median switch time by
the epoch duration to get the fraction of one epoch's earnings lost.
Example: 30 minute median switch + 22 hour epoch → `switch_cost_fraction
≈ 30 / (22 × 60) ≈ 0.0227` (2.3% of epoch earnings).

Different DAPI implementations may diverge slightly on these statistics
(different sample sizes, different outlier filters, different priors).
This is acceptable — agents are allowed to disagree about their
predictions; the market resolves correct ones through realized EV. The
GIP specifies defaults; implementers may tune.

#### 3.4. Switching intent announcements

When the agent has decided to switch, it submits `MsgDeclarePoCIntent`
for the target model before the switching INTENT cutoff (§2.3). The
announcement is read by other agents as a switching signal and consumed
in their `predicted_supply` computation.

When the agent has decided NOT to switch (staying on the current model),
no announcement is required. Stay-decisions are implicit from the
absence of an INTENT and the existing chain state showing the current
model.

The agent's published INTENT is the operator's binding commitment for
reputation purposes (§3.5). If the agent later changes its mind and runs
a different model, the reputation system marks the original announcement
as unfulfilled.

**Strategic bluffing.** A rational agent may sometimes deliberately
announce a model it does not intend to switch to. This is strategic noise
injection — by adding uncertainty to other agents' supply predictions,
the bluffer reduces the chance that competitors converge on the bluffer's
actual target. This GIP explicitly accepts strategic bluffing as
legitimate market behavior (see §Rationale and §Simulation Framework).
The reputation system is designed to tolerate it within bounds.

#### 3.5. Honesty reputation

Each rational agent maintains a local per-participant honesty score
computed from public chain data. No chain-side reputation, no chain-side
state.

**Truth rate.** For participant `p`:

```
truth_rate(p) = fulfilled(p) / total_verified(p)
```

where:

- `fulfilled(p)` counts INTENT announcements within the lookback window
  where p actually ran the announced model in the target epoch.
- A late fulfillment — announced model `m` for epoch `E`, ran model `m`
  in epoch `E+1` — counts as 0.5 fulfilled credit. The signal was
  directionally correct; the timing was off.
- `total_verified(p)` counts announcements within the window whose target
  epoch has passed (so the actual outcome is known).
- Lookback window: 20 announcements (default; governance-tunable).

**Reputation weight.** A continuous sigmoid function of truth_rate:

```
weight(p) = sigmoid((truth_rate(p) - threshold) / smoothness)
         = 1 / (1 + exp(-(truth_rate(p) - threshold) / smoothness))
```

with **canonical defaults**:

- `threshold = 0.70`
- `smoothness = 0.15`

These defaults are derived from agent-based simulation (§Simulation
Framework). The threshold is set above the natural strategic-bluffing
Nash equilibrium (~10-20% lying, i.e. ~80-90% truth_rate) so that
occasional bluffers retain near-full signal weight while systematic
liars are heavily discounted.

The continuous sigmoid rather than a binary threshold means a participant
who just got unlucky once (e.g., infrastructure failure during an
otherwise honest switch) doesn't suddenly lose all signal weight. They
take a smooth hit and recover as future announcements are honored.

**New participant default.** A participant with no verified announcement
history (i.e., they haven't made or completed an announcement in the
lookback window) is assigned a starting weight proportional to their
existing on-chain consensus weight relative to the network median:

```
weight_default(p) = 0.5 × min(1.0, consensus_weight(p) / median_consensus_weight)
```

This means:

- A brand-new node spinning up just to spam fake announcements has
  near-zero signal weight until they've accumulated real on-chain
  consensus weight.
- An established operator making their first announcement gets reasonable
  trust (≥0.5 weight) because they've already demonstrated commitment to
  the network in other ways.
- The combination of the announcement fee and the weight-proportional new-
  participant default provides Sybil hardening: a Sybil army needs both
  ngonka AND time-on-chain to move the market signal.

**Application in `predicted_supply`.** For epoch `E+1`, the agent
computes:

```
predicted_inflow(m, E+1)  = Σ over INTENTs for m at E+1:
                              announcer.throughput × weight(announcer)
predicted_outflow(m, E+1) = Σ over INTENTs that imply leaving m:
                              announcer.throughput × weight(announcer)
predicted_supply(m, E+1)  = current_supply(m) + predicted_inflow(m, E+1)
                                              - predicted_outflow(m, E+1)
```

Different DAPI implementations MUST converge on similar reputations
computed from the same public data. To prevent implementation divergence
that would make the announcement layer noisy, the **canonical reputation
computation (sigmoid formula, lookback window, late-credit weighting,
new-participant default)** is specified above and SHOULD NOT be varied
across implementations. The `switch_threshold` (§3.2) and the agent's
own lying rate are operator-tunable; the reputation formula is not.

#### 3.6. Operator temperament knobs and lazy-operator defaults

The agent exposes two first-class **operator temperament dials** that
shape how aggressively it reallocates. Neither is a security or
anti-herding mechanism; they're personal preferences for how the
operator wants their nodes to behave.

- **`switch_threshold` (aggressiveness)** — how much better next-epoch EV
  must be before the agent commits to a switch, on top of the projected
  switching cost. Default `0.05` (5%). Lower → more eagerly chase
  marginal EV gains; higher → more inertia, only switches when the
  signal is large.
- **`switch_cooldown` (stability)** — minimum number of epochs an MLNode
  waits after a switch before it can switch again. Default `5` epochs
  (~5 days at the current ~23-hour mainnet epoch). Lower → more responsive
  to demand shifts; higher → more stable operation. Some operators will
  run `1`; some will run `14`. The simulation finds the system robust
  across the full `0–15` range.

A DAPI with default configuration runs the agent automatically with
these defaults. Operators who don't tune anything get sensible behavior:

- Capability set = whatever models have weights present on disk and
  whose hardware floors the operator's MLNodes satisfy.
- `switch_threshold = 0.05`, `switch_cooldown = 5`.
- Switching-cost prior per §3.3.
- INTENT submitted automatically when the agent decides to switch.
- Reputation tracked locally; no operator action needed.

Operators who want manual control can override:

- **Pin to a specific model.** Reduces the capability set to size 1.
  Equivalent to disabling the agent for that MLNode; the operator has
  decided.
- **Adjust `switch_threshold` or `switch_cooldown`.** Per the temperament
  dials above.
- **Disable the agent entirely.** No auto-switching, no INTENT
  submission. Useful for operators who manage scheduling externally
  (e.g. via their own ops tooling).
- **Adjust personal lying rate.** Operators who want to engage in
  strategic bluffing can configure the agent's announcement layer to lie
  occasionally. The simulation suggests an equilibrium personal rate
  around 10-20%; defaults SHOULD be 0 (honest), with bluffing as an
  opt-in tuning knob for sophisticated operators.

#### 3.7. Model switch transition

A switch from model A to model B at epoch boundary `E → E+1`:

1. **During epoch `E`:** the MLNode continues serving model A. The agent
   has decided to switch and submitted INTENT before the switching INTENT
   cutoff.
2. **At the inference cutoff for epoch `E`:** stop accepting new inference
   requests on this MLNode. Drain in-flight requests.
3. **During the inter-epoch window:** mark the MLNode `STOPPED`, unload
   model A, load model B, mark ready. This is the wall-clock cost the
   agent estimated as `switch_cost_fraction × epoch_duration`.
4. **At epoch `E+1`'s PoC stage:** PoC model B. Serve model B for the
   remainder of `E+1`.

If the switch cannot complete in time (unexpected slow weight load,
network issues), the DAPI SHOULD continue serving the old model and
re-attempt the switch at the next epoch boundary. The late attempt
counts as a half-credit fulfillment for reputation purposes (§3.5).

The agent SHOULD reduce its observed switching-cost estimate for the
target model based on its own actual switch times. Self-observation
complements peer-observation in §3.3.

#### 3.8. Switch cooldown

An MLNode that switched models at epoch boundary `E → E+1` does not
switch again before epoch `E + 1 + switch_cooldown` (default `5` epochs,
~5 days on mainnet given the current ~23-hour epoch). The agent enforces
this locally; it is not a chain-level constraint and operators are free
to tune it.

`switch_cooldown` is an **operator stability preference**, not a
protocol-enforced anti-herding mechanism. The anti-herding work is done
by the announcement layer + EV math (§3.4–§3.5). Aggressive operators may
set `switch_cooldown = 1` and accept frequent reallocation; conservative
operators may set `switch_cooldown = 14` (~2 weeks) and accept slower
response to demand shifts. The simulation finds both extremes (and
everything in between, up to `switch_cooldown = 15`) give equivalent
earnings within noise — operators pick whatever fits their operational
temperament.

Note: `switch_cooldown` is a per-MLNode constraint, not a per-participant
one. An operator running multiple MLNodes can effectively rotate models
across the fleet at any cadence by spinning new nodes onto new models
rather than reconfiguring existing ones. That is intentional — operators
with more hardware naturally have more agility, and the EV math +
reputation system still constrain the fleet as a whole through its
visible PoC supply and announced intents.

#### 3.9. PoC validation interaction

A multi-model PoC requires the validating MLNode to actually run the
model it is being PoC'd against. Combined with §3.7, this gives an
unambiguous rule:

- **The model an MLNode PoCs at epoch `E+1`'s PoC stage is the model it
  serves at the start of epoch `E+1`.**
- A switch initiated for the `E → E+1` boundary takes effect *before*
  the PoC stage of `E+1` (per the §3.7 timeline). The new model is
  what gets PoC'd.
- A switch that fails to complete in the inter-epoch window leaves the
  MLNode on the old model; it PoCs the old model. The agent re-attempts
  the switch for `E+1 → E+2`.

This means a successful switch costs at most one PoC cycle of revenue at
the new model (the first PoC on the new model is the first PoC ever).
This is a real cost reflected in `switch_cost_fraction` (§3.3).

For `LEARNING` models with too few hosts to form a PoC quorum, the paired
validation-grace GIP applies (see Open Issues §1).

### 4. Layer 2: Adaptive coefficient

#### 4.1. Effective coefficient formula

For each model `m` in epoch `E`:

```
effective_coeff(m, E) = clamp(
    base(m) × demand_factor(m, E) × status_factor(m, E),
    band_min(m, E),
    band_max(m, E)
)
```

where:

- `base(m)` is `weight_scale_factor` from `PoCModelConfig`.
- `demand_factor(m, E)` and `status_factor(m, E)` are defined below.
- `band_min` and `band_max` are the LEARNING-period bands while
  `current_epoch < penalty_start_epoch`, the standard bands otherwise.

The result is what consensus-weight aggregation MUST use in place of the
static `weight_scale_factor`. The on-chain
[`ConfirmationWeightScale`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/x/inference/types/weight.go)
record SHOULD be extended to carry the effective coefficient so it is
observable per epoch by both consensus and by rational agents.

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

##### Sparsity fallback

If the trailing-window total inference count is too low to produce a
reliable demand signal, `demand_factor` falls back to `1.0` across all
models:

```
if total_inference_count_trailing(D) < n_inferences_threshold:
    demand_factor(m, E) = 1.0   for all m
```

`n_inferences_threshold` is a governance-set parameter (default `1000`,
i.e. ~1000 inferences across the entire network over the trailing
window). Below this floor the coefficient sticks at `base × status_factor`
and operators react to governance-set baseline values and supply ratios
alone — equivalent to how the current static `weight_scale_factor`
mechanism behaves today.

This fallback is critical during the bootstrap period when paid-inference
volume is small. Without it, `share(m) / s_ref` would be dominated by a
handful of payers and the coefficient would be noisy and gameable. With
it, the system gracefully degrades to current behavior until inference
revenue scales up, at which point `demand_factor` activates and the
coefficient becomes responsive to settled inference value. The threshold
SHOULD be set by governance at upgrade time to roughly 5–10× the typical
trailing-window total inference count at the moment of activation, so
the mechanism activates only when there's enough volume to be meaningful.

#### 4.3. The learning period (LEARNING) and uncertainty bands

The LEARNING phase is **derived from the existing
`penalty_start_epoch`** defined in
[multi-model PoC](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md):
a model with `current_epoch < penalty_start_epoch` is in LEARNING; once
`current_epoch >= penalty_start_epoch` it is governed by its `status`
field (ACTIVE by default).

Reusing this boundary keeps a single source of truth for "this model has
graduated from bootstrap." The same epoch at which participation
penalties begin applying is the epoch at which the wide uncertainty band
tightens.

While LEARNING:

- The effective coefficient is clamped to `[min_band_learning,
  max_band_learning]` rather than the standard `[min_band, max_band]`.
- These bands are **NOT** derived as multiples of `base`. They are set
  explicitly by governance at model registration, sized to make the new
  model genuinely EV-positive for first-mover operators given the
  expected switching cost and the existing reward landscape.
- The wider band is **the bootstrap subsidy** in this design. With no
  chain-side scheduler directing operators to new models, the only way a
  rational agent gets pulled toward an unproven model is via a coefficient
  high enough to make the switch EV-positive even with a conservative
  switching-cost prior.

**Why explicit, not derived as a multiple of `base`.** A naive design
that sets `max_band_learning = 2.0 × base` produces perverse outcomes for
low-`base` models. Concrete example from current mainnet: MiniMax-M2.7 was
registered with `weight_scale_factor = 0.30`. A `2.0 × base` ceiling
gives `max_band_learning = 0.60`, which is *still less than* the existing
Qwen-235B-FP8 base coefficient of `0.36`. A new model that pays
proportionally less per PoC unit than the incumbent during its learning
window has no realistic chance of attracting early adopters — they
would lose money switching to it.

Governance MUST set the LEARNING ceiling at a value attractive relative
to **the network's existing reward landscape**, not relative to the new
model's own base. Suggested target: `max_band_learning ≥ 1.5 ×
network_median_current_coefficient` at the time of model registration.
That ensures a first-mover operator switching to the new model has a
clear EV improvement over staying on whatever model their hardware was
previously serving, sized to cover the switching-cost prior plus margin.

At `current_epoch == penalty_start_epoch`:

- The standard `[min_band, max_band]` takes over.
- `[min_band, max_band]` MAY be derived from `base` as multiples (e.g.,
  `[0.5 × base, 2.0 × base]`) since by that point a model has demand
  history and operators can react to `demand_factor` directly. The
  multiplicative-on-base derivation is fine post-graduation because
  `base` itself has been validated by the LEARNING period.

No new graduation logic or epoch counter is introduced; everything keys
off `penalty_start_epoch`, which governance already sets per model.

#### 4.4. `status_factor`

| status | status_factor |
|---|---|
| `ACTIVE` | 1.0 |
| `DEPRECATED` | linear decay from 1.0 to 0.0 over deprecation window `W_d` (default `10` epochs) |
| `RETIRED` | 0.0 |

(LEARNING is not a `status` value; learning models default to `ACTIVE`
status and the LEARNING-vs-ACTIVE distinction is handled via
`current_epoch < penalty_start_epoch` and the wider band per §4.3.)

### 5. Lifecycle

#### 5.1. State machine

The bootstrap → active transition is governed by the existing
`penalty_start_epoch` field and is not modified by this GIP. This GIP
adds the ACTIVE → DEPRECATED → RETIRED end-of-life states.

```
   (bootstrap, governed by multi-model PoC and penalty_start_epoch)
                                │
                                │ current_epoch >= penalty_start_epoch
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

LEARNING is not a `status` enum value; it is the derived condition
`current_epoch < penalty_start_epoch` and applies regardless of the
`status` field's value (which is `ACTIVE` by default at model
registration).

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

A `RETIRED` model is removed from PoC entirely; its `effective_coeff` is
`0`, it is excluded from `EpochModelDemandSummary` aggregation going
forward, and rational agents MUST drop it from their capability sets.
DAPIs SHOULD switch any MLNode currently serving a RETIRED model off it
within `switch_cooldown` epochs.

#### 5.5. Governance overrides

Per [voting.md](https://github.com/gonka-ai/gonka/blob/main/docs/voting.md),
Gonka has two governance levels: slow **Governance Voting** (x/gov,
days-long, intended for protocol-significant changes) and fast
**Operational Voting** (x/group, minutes-long, used for inference and
PoC validation operational decisions).

This GIP recommends:

- **Governance Voting (x/gov)** for: changes to a model's `min_gpu_count`,
  `min_vram_gb_total`, or `penalty_start_epoch`; forcing status transitions
  between ACTIVE / DEPRECATED / RETIRED; veto of an automatic retirement;
  changes to the canonical reputation parameters (`threshold`, `smoothness`,
  `lookback_window`) since divergence across implementations would
  fragment the market signal.
- **Operational Voting (x/group)** for: routine tuning of global parameters
  (`D`, `W_q`, `W_d`, `W_v`, `s_ref`, deprecation/recovery thresholds,
  `demand_factor` bounds, per-model band widths, the switching INTENT
  cutoff). These have bounded effects, are reversible, and benefit from
  the faster cadence.

This split is a recommendation; final partition between voting modes
SHOULD be confirmed by the chain's governance maintainers at upgrade time.

### 6. Genesis and migration

At the upgrade introducing this GIP:

1. Every existing `PoCModelConfig` MUST be backfilled with:
   - `status = ACTIVE`. Existing models already have `penalty_start_epoch`
     set by prior governance; this GIP does not reset it. The founding
     model (`Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`, current
     `initial_model_id`) retains its existing weight-cap exemption from
     multi-model PoC and is not affected by this GIP's lifecycle additions.
   - `status_changed_at_epoch = current_epoch`.
   - `min_band = base × 0.5`, `max_band = base × 2.0`.
     `min_band_learning` and `max_band_learning` MUST be set explicitly
     per the calibration pass below (NOT derived as multiples of `base` —
     see §4.3 for why).
   - `min_gpu_count` and `min_vram_gb_total` set per the table in §2.2 for
     the three currently-known models. Models added between this GIP
     being drafted and shipped MUST be assigned values by governance at
     upgrade time.

2. **Pre-activation calibration pass (governance).** Before this GIP
   activates, governance MUST perform a deliberate calibration pass on
   the parameters that become more load-bearing post-upgrade. These were
   set under the pre-GIP model where they functioned as static per-model
   multipliers with no `demand_factor` or `status_factor` reading on top;
   post-GIP they directly determine reward share whenever the demand
   signal is sparse (which is the current state of mainnet).

   The calibration pass SHOULD:

   - **Confirm `weight_scale_factor` (i.e. `base`) values for every
     currently-active model.** Current mainnet values (`Qwen ≈ 0.36`,
     `Kimi K2.6 ≈ 0.78`, `MiniMax-M2.7 ≈ 0.30`) reflect the pre-GIP
     intuition of relative hardware difficulty. With Layer 1's local
     rational agent active, operators will have more freedom to choose
     among models, so these ratios become the *primary* incentive for
     which model an operator on capable hardware will run. Are they where
     governance wants them?
   - **Set `[min_band_learning, max_band_learning]` per active model
     explicitly.** Per §4.3, the LEARNING ceiling must be attractive
     relative to the network's existing reward landscape, not relative to
     the new model's own `base`. Recommended target: `max_band_learning
     ≥ 1.5 × network_median_current_coefficient` at the time of model
     registration. For models that have already graduated
     (`current_epoch >= penalty_start_epoch`), the LEARNING bands are
     never exercised again so backfilling them to equal the standard
     bands is fine; for models still in their LEARNING window at upgrade
     time, the LEARNING bands MUST be set explicitly.
   - **Set `n_inferences_threshold` (§4.2 sparsity floor) at a value
     reflecting current mainnet inference volume.** A conservative
     starting point is roughly 5–10× the typical trailing-window
     (default `D = 7` epochs) total inference count, so the
     `demand_factor` only activates during meaningful traffic. At current
     mainnet volume this is likely in the low thousands; governance
     SHOULD set the actual number based on observed inference counts at
     the moment of upgrade.

   These are governance values, not protocol constants. The GIP ships
   the *mechanism*; governance ships the *initial values*. The
   calibration pass SHOULD produce a parameter-change proposal bundled
   with the upgrade artifact itself.

3. INTENT handling for active models is extended per §2.3 at the
   upgrade. INTENT submitted for an active model before the upgrade
   continues to be a no-op (it was a no-op before); INTENT submitted
   after the upgrade is a switching announcement.

4. `EpochModelDemandSummary` MUST begin populating at the first epoch
   after the upgrade. The trailing-window demand factor reads as `1.0`
   (neutral) until at least `D` epochs of history exist.

5. Existing DAPIs continue functioning unchanged. The rational agent is
   an optional DAPI feature that operators opt into by upgrading to a
   DAPI build that includes it. Until they do, their MLNodes continue
   serving their `ENFORCED_MODEL_ID` configuration. The agent layer is
   advisory and DAPI-local; there is no protocol-level adoption
   requirement.

6. Layer 2 (adaptive coefficient) takes effect immediately at upgrade
   with the calibrated values from step 2. With migration defaults
   (`demand_factor` reads as 1.0 until enough history accumulates AND/OR
   the sparsity floor is binding, `status_factor = 1.0`), the effective
   coefficient equals `base` immediately post-upgrade. Divergence
   develops only as demand history accumulates above the sparsity floor.

## Rationale

### Why local rational agent rather than chain-published scheduler

An earlier draft of this GIP proposed a chain-side scheduler that
published per-epoch opinions (target compute share per `(GPU class,
model)` pair) consumed by DAPIs. That approach had several structural
problems the local-agent approach solves:

- **The chain cannot verify what it would direct.** Chain-side scheduling
  needed an on-chain GPU class registry, on-chain `MLNodeCapability`
  declarations, and on-chain `target_gpu_class` per model. None of these
  declarations are cryptographically verifiable — an operator could
  declare any GPU class. The scheduler's opinion would then be only as
  good as the (unverified) declarations underneath it.
- **The agent knows what the chain can't.** Each operator's DAPI already
  knows the operator's hardware exactly (no declaration needed), knows
  which model weights are cached on disk, knows the operator's risk
  tolerance and operational constraints. Pushing the decision logic to
  where the information lives gives richer decisions and avoids needing
  to encode every relevant detail in chain state.
- **Consensus-critical complexity goes away.** A chain-side scheduler
  must produce identical output on every node: no floating-point math,
  ordered map iteration, explicit tiebreaking. An agent-local
  computation has no such constraint. Different DAPI implementations may
  make different decisions from identical chain state, and that's fine.
- **Removing the scheduler removes the GPU class registry, capability
  declarations, and `EpochSchedulerTarget` as on-chain state.** Much
  smaller surface area for bugs, attacks, and governance overhead.
- **The trade-off is small.** Chain-side scheduling would have been
  *enforceable* (the chain says "this is the opinion, here's what
  compatible MLNodes should do"). Agent-local is *advisory* — each
  operator does what their agent computes. The simulation work
  (§Simulation Framework) shows that under the expected equilibrium
  conditions, the agent-local design converges to ≥95% market efficiency.
  Comparable to what a chain-side scheduler could achieve in theory.

### Why INTENT becomes meaningful for active models

In multi-model PoC, `MsgDeclarePoCIntent` is meaningful only for
bootstrap (not-yet-active) models — declaring "I plan to deploy this
model that hasn't been activated yet." For active models, INTENT is
currently ignored.

This GIP extends INTENT semantics so that an INTENT for an active model
functions as a switching announcement. Other rational agents read it from
the chain and adjust their predicted-supply calculations. The extension
is intentionally minimal:

- **No new message type.** Reuses `MsgDeclarePoCIntent` with extended
  semantic. Same indexing infrastructure, same chain-side accept path.
- **No new chain state.** Announcements are already indexed by epoch and
  participant.
- **No chain-side reputation.** Reputation is local to each agent. The
  chain just publishes raw announcement data.

The fee on switching-mode INTENT is intentionally not exempt. Unlike
bootstrap INTENT (closer to a network duty for new models), switching
INTENT is a market signal. Making it slightly costly prevents spam
without deterring honest use, and the cost is small enough that strategic
bluffing — when an operator's EV math justifies it — remains viable.

### Why a continuous-discount reputation rather than a binary filter

A binary filter ("if your truth_rate < threshold, ignore your signal
entirely") is too brittle. Real operators occasionally have legitimate
reasons their announcement doesn't match their actual model (hardware
failure mid-switch, network issue, late re-deploy). A single mistake
shouldn't permanently silence them.

A continuous sigmoid discount lets the system:

- Tolerate occasional unlucky honest operators (one bad epoch out of 20
  → truth_rate = 0.95 → near-full weight).
- Discount strategic bluffers gracefully (truth_rate ≈ 0.8 → still useful
  but reduced).
- Effectively silence systematic liars (truth_rate < 0.5 → minimal
  signal weight).
- Avoid knife-edge behavior at any single threshold value.

The sigmoid's `smoothness` parameter is structurally more important than
its `threshold`. A high smoothness ensures even a participant near the
threshold contributes some signal, preserving information. A very sharp
sigmoid approximates a binary filter and is less robust.

### Strategic bluffing as expected behavior

A rational operator may sometimes deliberately announce a model they
don't plan to switch to. This is strategic noise injection — by adding
uncertainty to other agents' supply predictions, the bluffer reduces the
chance that competitors converge on the bluffer's actual target.

This GIP **explicitly accepts strategic bluffing as legitimate market
behavior**, not an exploit to suppress. The reputation system distinguishes
occasional bluffers (truth_rate ≈ 0.8) from systematic liars (truth_rate
< 0.5) and discounts only the latter heavily.

The simulation found a Nash equilibrium personal lying rate of ~10-20%
under realistic conditions (limited information, gossip latency).
Meaning a population of rational operators converges to bluffing on
roughly 1 in 5-10 announcements. This is the expected market state, not
a pathology. The system was designed with this in mind.

### Why two layers

Conflating equilibrium and pricing into a single composite multiplier
forces every host to be a savvy market participant. In practice most
network compute belongs to operators who configure their nodes once and
rarely revisit. A composite multiplier produces correct behavior only
when hosts respond to it. Splitting the design into a default rational
agent (which allocates without requiring operator response) and an
adaptive coefficient (which prices for those agents to respond to)
ensures equilibrium even when most operators are passive.

The agent makes the lazy-operator case work. Even an operator who never
touches their DAPI gets sensible scheduling from the agent's defaults.
The coefficient layer makes the savvy-operator case work — those who pin
models manually arbitrage the coefficient signal directly.

### Why settled paid value rather than token count

Tokens correlate with compute but not with what the network actually
collected. Paid value reflects both volume and price, captures the
demand-side willingness to pay, and is harder to game than token count
(which can be inflated with cheap or self-directed traffic).

### Capability adoption replaces the bootstrap problem

In a directive-scheduler design, bootstrapping a new model means
"convince hosts to switch onto it." In an agent-local design,
bootstrapping a new model means "set the LEARNING-band ceiling high
enough that a rational agent's EV math justifies the switching cost for
a few brave operators." That's a meaningfully different (and more
honest) problem:

- The chain cannot allocate compute that no operator's agent finds
  EV-positive, regardless of how attractive the coefficient is in the
  abstract. But: the LEARNING-band makes the coefficient *concretely*
  attractive — if `effective_coeff(new model) = 2× base`, the rational
  agent's EV math may pull operators toward it even with no demand
  history.
- The first deployments need an off-chain or off-protocol push (the
  model proponent coordinating with hosts). That push is a coordination
  problem, not a protocol parameter problem.

### The savvy/lazy dynamic is intentional

A savvy operator can ignore the agent entirely by pinning their MLNode
to whichever model has the highest `effective_coeff`. They capture the
high-revenue spot; the lazy operators (running the agent with defaults)
absorb the rebalancing the agent does to keep other models served. This
is by design rather than a flaw:

- The savvy operator is doing exactly what a market participant should
  do — responding to a price signal. The point of the coefficient layer
  is to provide that signal.
- The lazy operator gives up some upside in exchange for not having to
  monitor coefficients themselves. They get the average; the savvy
  operator gets the peak. That trade-off is the operator's choice.
- The network gets equilibrium because most operators are lazy and the
  agent's allocation across them produces balance. A few savvy operators
  arbitraging the margins do not break this — they are a small fraction
  of supply.

This dynamic does mean the agent layer is **only useful when most
operators run the agent with defaults**. If every operator pins their
MLNode manually, the agent is a no-op and the network's equilibrium
depends entirely on the coefficient layer plus operator response. That
state is degenerate but not broken; it's just the system without
Layer 1's benefit.

### Why no on-chain GPU class taxonomy

The previous draft proposed an on-chain GPU class registry to support
fit-factor scheduling. The registry was load-bearing but the underlying
declarations were unverifiable: an operator could declare any class. The
fit factor depended on the registry's ordering being accurate, but the
ordering was a governance vote, and the declarations against the
ordering were operator self-reports.

Without a chain-side scheduler, the fit-factor logic isn't needed at the
chain layer. Each rational agent knows its own hardware exactly and can
encode its own preference for matching high-end GPUs to high-end models
in its EV math (e.g., by weighting throughput on a flagship model
running on flagship hardware more highly than the same model on
mid-tier). This preference emerges naturally from local EV
maximization, without needing the chain to encode it.

### Why per-host quality routing is out of scope

By "per-host quality routing" we mean: for each `(host, model)` pair,
tracking signals like p95 latency, error/timeout rate, cancel rate, and
validation pass rate, then biasing inference-request routing toward
hosts with a better track record on the requested model. It changes
*who gets the work*, not *what the work is worth*.

This GIP treats all hosts on a given model as interchangeable for
coefficient purposes. Per-host quality is a routing-layer adjustment
that is largely independent of the consensus-weight rebalancing here.
The signals it needs are not currently settled chain state, the gaming
surface is materially larger than for the per-model demand factor, and
the natural prototype location is the DAPI's request-routing logic
before any chain-state promotion. Bundling it would expand scope without
improving either system. A follow-on GIP can specify per-host quality
once layers 1 and 2 are operating in production.

## Backwards compatibility

This GIP is a consensus-affecting change (Layer 2) and requires a
coordinated upgrade. Layer 1 is purely DAPI-side and is not
consensus-affecting.

- The static `weight_scale_factor` field is preserved as the `base`
  parameter; existing values are not lost.
- Consensus-weight aggregation switches from `weight_scale_factor` to
  `effective_coeff`. With the migration defaults (`demand_factor` reads
  as `1.0` until enough history exists, `status_factor = 1.0` for active
  models), the effective coefficient equals the base immediately
  post-upgrade. Behavior is unchanged at the upgrade boundary; divergence
  develops only as demand history accumulates.
- The semantic change to INTENT (§2.3) is additive: INTENT for active
  models was previously a no-op and becomes a switching announcement.
  Operators who don't submit any INTENTs for active models see no
  difference.
- DAPIs that do not implement the rational agent (§3) continue
  functioning with operator-configured model assignments. The agent is
  an optional DAPI feature; running without it is not a protocol
  violation.
- The legacy `HardwareNode.hardware[].type` field is preserved unchanged
  for informational purposes; it is not consumed by this GIP.

## Security considerations

### Demand metric gaming

The demand factor responds to settled paid value. An attacker controlling
both a payer account and a host could submit self-directed traffic to
inflate a model's demand factor and steal coefficient share from
legitimate models. Mitigations:

- Only `valid`-status, paid, non-refunded inferences count.
- The demand factor is bounded (`clamp(·, 0.5, 2.0)` by default), so
  even a successful inflation attack has a bounded multiplicative effect
  on the coefficient.
- Settled inference fees represent a real cost to the attacker; the
  attack is not free.

A future hardening could require demand attribution to come from
developer/TA accounts above some reputation threshold, or weight demand
by the diversity of paying accounts. Not specified here.

### Announcement gaming

A participant could spam fake switching announcements to perturb other
agents' supply predictions. The reputation system penalizes this: the
spammer's truth_rate drops, their signal weight in other agents' EV math
drops with it, and their announcements become ineffective.

The new-participant default reputation (§3.5) is proportional to
existing on-chain consensus weight, so spinning up fresh participants for
spam announcements has near-zero market impact. Combined with the
INTENT transaction fee, this provides Sybil-resistant signal
authentication without any chain-side reputation machinery.

### Strategic bluffing escalation

In principle, a population could collude to systematically bluff their
announcements in a way that degrades the entire announcement layer's
information value. The reputation system makes this self-defeating: if
everyone's truth_rate is low, everyone's reputation weight is low, the
announcement layer carries little weight in EV computations, and agents
fall back to direct supply observation. The system gracefully degrades
to the baseline (no announcement layer) rather than producing pathological
allocations.

The simulation (§Simulation Framework) confirms that even in a population
where everyone lies 80% of the time, individual operators have strong
incentive to deviate toward honesty — gains of +5 to +30% EV are
available to operators who lie meaningfully less than the median. The
equilibrium pushes back toward moderate honesty.

### Bounded damage from learning-period models

A newly approved model in the learning period
(`current_epoch < penalty_start_epoch`) is bounded on two axes:

- Coefficient bounded by `min_band_learning` and `max_band_learning`
  (wider than the post-graduation band but still finite).
- Voluntary adoption: no operator is forced to deploy a learning model;
  rational agents only switch to it if their EV math justifies it
  including the conservative LEARNING-period switching-cost prior. Bad
  models attract no adoption and self-quarantine.

### Auto-deprecation of useful niche models

A genuinely useful but low-volume model could fall below
`deprecation_threshold` and be auto-deprecated. The veto window `W_v`
(default 20 epochs) gives governance time to intervene before retirement
becomes terminal. Recovery from `DEPRECATED → ACTIVE` is automatic if
demand returns above `recovery_threshold` before retirement.

### Reputation parameter divergence

If different DAPI implementations compute reputation slightly differently
(different lookback windows, different sigmoid functions, different
late-credit weighting), agents disagree about which announcements to
trust. This noise degrades the announcement layer's information value.

Mitigation: the canonical reputation parameters (lookback = 20, sigmoid
threshold = 0.70, smoothness = 0.15, late credit = 0.5) are specified
above and SHOULD NOT be varied across implementations. Operators can tune
their personal `switch_threshold` or personal lying rate locally; they
should not tune the reputation function.

### Validation interaction (paired GIP)

Learning-period models with very few hosts cannot pass standard PoC
validation alone. This GIP defers the validation grace mechanism to a
paired GIP (see §Open issues). Until that paired GIP ships, the
practical constraint is that a model SHOULD NOT be approved unless at
least the minimum-quorum number of hosts have committed to deploying it.

## Phased rollout

**Phase 1 (Layer 2 only).** Adaptive coefficient with bands and
`demand_factor`. New on-chain state: `EpochModelDemandSummary`,
PoCModelConfig band/status extensions. Layer 1 not yet shipped; DAPIs
continue operating in legacy enforced-model mode. Revenue is now
responsive to demand. Bands kept conservative initially. **Consensus-
affecting; requires governance upgrade.**

**Phase 2 (Layer 1).** Rational agent ships in the DAPI. INTENT semantic
extension for active models ships on-chain (the only Phase 2 consensus
change). Operators opt in by upgrading their DAPI. As adoption grows, the
announcement layer's information value rises and equilibrium behavior
emerges. **Minor consensus change for INTENT semantics; DAPI changes
are opt-in.**

**Phase 3 (lifecycle).** Auto-deprecation and retirement state machine
ships. Requires paired validation-grace GIP for full functionality.
**Consensus-affecting; requires governance upgrade.**

## Simulation Framework

To validate the parameter choices in §3.5, an agent-based simulation was
conducted. Code lives in
[`simulation/`](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-scheduling/simulation/).
An **interactive HTML simulation** at
[`simulation/index.html`](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-scheduling/simulation/index.html)
runs in any modern browser — operators visualize on a grid, demand and
supply animate as line charts, and a control panel exposes every parameter
in §3 so reviewers can verify the GIP's defaults behaviorally. A separate
"Why these defaults?" tab presents pre-computed parameter sweeps showing
the system is robust to parameter choice within wide ranges around each
default.

The Python simulation setup models:

- 100 operators across 3 hardware tiers (small/mid/flagship with
  throughput ratios 1:4:10).
- 5 models with tier requirements and fluctuating demand (overlapping
  sine waves + occasional demand shocks).
- Per-epoch iterated cheap talk: each agent decides plan, reads others'
  announcements (weighted by reputation), updates plan, announces.
- Honesty reputation per the sigmoid (§3.5).
- 600 epochs per run, 200-epoch warmup.

Four realism regimes were tested:

1. **Clean (idealized):** 10 iterations of cheap talk per epoch, full
   information (each agent sees all others' announcements).
2. **Reduced iterations:** 2 iterations per epoch, full information.
   Models limited within-epoch convergence time.
3. **Partial information:** 10 iterations, each agent sees a random
   sample of 20/100 others' announcements. Models gossip latency.
4. **Realistic (combined):** 2 iterations + partial information.

### Population-optimal lying rate

Across every `(threshold, smoothness)` combination in the sweep, market
efficiency (fraction of total value pot captured by the network) peaks at
L = 0 (population lying rate of 0 → efficiency 100%) and degrades
monotonically as L rises. Bluffing creates pure noise in the announcement
signal that hurts everyone collectively. The "some bluffing is good for
the market" intuition does NOT hold at the population level — it's
strictly destructive of collective welfare.

### Nash equilibrium under realistic friction

While the population-optimal is L = 0, individual incentive to bluff is
real under realistic frictions. At `threshold = 0.7, smoothness = 0.10`:

| Regime | Nash L* | Best deviator gain at pop_L = 0 |
|---|---|---|
| Clean | — (no interior Nash) | +0.24% |
| Reduced iterations | **L\* = 0.2** | +0.38% |
| Partial information | — (no interior Nash) | **+18.22%** |
| Realistic (combined) | **L\* = 0.1** | +0.65% |

The most striking observation: under partial information (each operator
sees only 20% of others' announcements), a single operator who bluffs
at L = 0.15 in an otherwise-honest population gains an 18% EV advantage.
That's a strong individual incentive. Under the more realistic combined
case (limited iteration AND partial info), Nash converges to L ≈ 0.10 —
exactly the "10-20% strategic bluffing" range we'd expect.

### Robustness of the discount function

Across the threshold sweep at fixed smoothness = 0.20, efficiency at
moderate lying rates (say L = 0.3) varies by less than 2% across
`threshold ∈ [0.5, 0.9]`. The discount function's *shape* (smoothness)
matters more than the *inflection point* (threshold).

Higher smoothness is uniformly better for system robustness — at high L,
efficiency degrades less with smoothness = 0.20 than with smoothness =
0.05. The smoother sigmoid preserves more information from operators
whose truth_rate is below threshold rather than abruptly zeroing them
out.

### Systematic liars self-correct

At pop_L = 0.8 across all configurations, a deviator's best response is
L ≈ 0.10-0.15, with gains of +2% to +24% EV. Even in a totally dishonest
market, individual operators have strong incentive to be mostly honest.
The reputation system self-stabilizes the population away from systematic
lying.

### Parameter choice

The simulation supports the defaults specified in §3.5:

```
threshold:                       0.70
smoothness:                      0.15
lookback window:                 20 announcements
new-participant default rep:     0.5 × min(1, weight / median)
late-credit fraction:            0.5
expected equilibrium personal L: 10-20% (strategic bluffing)
```

### Robustness of the defaults

A separate sweep over every GIP-tunable parameter under adversarial
conditions (**256 operators across 2000-epoch runs**, partial information
of 5 visible others per agent, aggressive demand volatility — chosen to
make parameter sensitivity visible if it exists) finds that **the system
is largely insensitive to parameter choice within reasonable ranges**:

- Sweeping `threshold ∈ [0.1, 0.99]`, `smoothness ∈ [0.01, 1.0]`,
  `lookback ∈ [3, 100]`, `switch_threshold ∈ [0.001, 0.5]`,
  `late_credit ∈ [0, 1]`, `new_participant_default ∈ [0, 1]` — mean
  per-operator earnings vary by under 1% across the entire range.
- Only `switch_cooldown` at extreme values (≥30 epochs, ~1 month) clearly
  degrades performance (~3% earnings drop), because it locks operators
  into the wrong model for too long when conditions change.
- Under a fully-dishonest population (`pop_lying_rate = 1.0`), earnings
  hold steady (within noise of baseline). The reputation system
  gracefully degrades to "use current supply only" rather than
  catastrophically failing.

This is the property the design aims for. The defaults are chosen as
sensible round numbers in the robust zone, not magic values that the
system is sensitive to. Operators tuning their own DAPI within the
expected ranges (e.g., `switch_threshold ∈ [0.02, 0.10]`, or
`switch_cooldown ∈ [3, 10]`) won't hurt themselves; only obviously-extreme
values do.

Reviewers MAY propose alternative defaults by running
[`defaults_sweep.py`](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-scheduling/simulation/defaults_sweep.py)
with modified ranges and presenting the resulting earnings curves. The
interactive simulation can be used to verify proposed parameters
behaviorally.

## Open issues

1. **Validation grace for learning-period models** is deferred to a
   paired GIP. Without it, the practical pre-condition for approving a
   new model is that enough hosts have pre-committed to deploying it to
   satisfy PoC validation quorum from epoch one of the learning period.

2. **DAPI rational-agent adoption.** Layer 1 is opt-in via DAPI upgrade.
   Equilibrium behavior emerges only as adoption grows. Off-protocol
   coordination (operator outreach, documentation, default-enabled in
   new DAPI releases) is required to drive adoption. A future GIP MAY
   introduce on-chain incentives for running the agent if adoption stalls.

3. **Default parameter values** (`D`, `s_ref`, `n_inferences_threshold`,
   `switch_cooldown`, deprecation/recovery thresholds, standard band
   widths, reputation parameters) given in this spec are starting points.
   Final values SHOULD be set after testnet observation and MAY be revised
   post-deployment via the appropriate governance level (see §5.5).
   `weight_scale_factor` per model, `[min_band_learning, max_band_learning]`
   per active model, and `n_inferences_threshold` require a pre-activation
   calibration pass per §6 step 2.

4. **Per-host quality routing** is acknowledged as a third layer above
   the two specified here and is deferred to a follow-on GIP. Off-chain
   prototyping in the DAPI is encouraged in the interim.

5. **GIP numbering and publication venue.** Gonka does not yet have a
   formalized GIP numbering scheme or a single canonical proposal
   discussion venue documented in
   [voting.md](https://github.com/gonka-ai/gonka/blob/main/docs/voting.md)
   or
   [prepare-upgrade-proposal.md](https://github.com/gonka-ai/gonka/blob/main/docs/prepare-upgrade-proposal.md).
   This proposal uses the placeholder `GIP-XX` and lives in
   [`proposals/multi-model-scheduling/`](https://github.com/gonka-ai/gonka/tree/main/proposals/multi-model-scheduling)
   matching the existing convention for design docs. When implementation
   ships, the upgrade artifact should also live in
   `proposals/governance-artifacts/update-vX.Y.Z/` per current
   convention. Establishing a GIP numbering scheme is left to a separate
   process proposal.

## References

- [Multi-Model PoC proposal](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md)
- [`PoCModelConfig.weight_scale_factor`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/params.proto#L140-L154)
- [`weight.go` consensus-weight aggregation](https://github.com/gonka-ai/gonka/blob/main/inference-chain/x/inference/types/weight.go)
- [`MsgDeclarePoCIntent`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/tx.proto)
- [`Inference.model` field](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/inference.proto#L37)
- [`EpochPerformanceSummary`](https://github.com/gonka-ai/gonka/blob/main/inference-chain/proto/inference/inference/epoch_performance_summary.proto)
- [`enforced_model.go`](https://github.com/gonka-ai/gonka/blob/main/decentralized-api/broker/enforced_model.go)
- [Gonka governance / voting](https://github.com/gonka-ai/gonka/blob/main/docs/voting.md)
- [Upgrade proposal preparation](https://github.com/gonka-ai/gonka/blob/main/docs/prepare-upgrade-proposal.md)
- [Gonka PoC overview](https://github.com/gonka-ai/gonka/blob/main/docs/gonka_poc.md)
- [Gonka tokenomics](https://github.com/gonka-ai/gonka/blob/main/docs/tokenomics.md)
- [Multi-model PoC `penalty_start_epoch` and participation modes](https://github.com/gonka-ai/gonka/blob/main/proposals/multi-model-poc/README.md)
- [Simulation framework (this directory)](https://github.com/gonka-ai/gonka/tree/main/proposals/multi-model-scheduling/simulation)
