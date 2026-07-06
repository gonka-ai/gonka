# Previous-Epoch Confirmed Weight Cap for Consensus Trust

## Overview

This proposal limits how quickly a participant's **trust weight** — the weight used for governance voting, BLS threshold signing, and cPoC (confirmation PoC) validation voting power — can grow between epochs. A participant's trust weight for an epoch is capped at the **confirmed weight they actually proved in the previous epoch**. A participant that jumps its declared compute significantly must first prove that new capacity for a full epoch before the increase counts toward consensus-critical decisions.

Crucially, this cap does **not** affect rewards. A participant that legitimately increased its capacity still earns rewards on its real, fully-adjusted weight (after all cPoC settlement) during the very first epoch. Only its influence over consensus (governance, BLS, cPoC validation voting) is delayed by one epoch.

As described in [gonka_poc.md](../../docs/gonka_poc.md), Gonka operates two power systems: the staking/CometBFT power that governs consensus and governance, and the epoch-group power that governs PoC validation, inference allocation, and rewards. This proposal tightens the first (trust) system while leaving the reward system untouched.

## Problem Statement

A participant's declared/proven weight can increase dramatically from one epoch to the next (e.g. by bringing large amounts of hardware online, or by manipulating PoC). Under the previous behavior, that new weight immediately translated into:

- **Governance voting power** — the ability to push or block proposals.
- **BLS threshold-signing share** — influence over the network's threshold signatures.
- **cPoC validation voting power** — influence over which PoC results are accepted, i.e. over what everyone else's weight is validated to be.

A sudden, unproven jump in these areas is a security risk: a participant could acquire outsized consensus influence in a single epoch, before the network has had a chance to confirm (via a full epoch of cPoC) that the claimed capacity is real. The most dangerous case is cPoC validation voting power itself — a participant with an unproven weight spike would immediately gain more say in validating everyone's weights.

## Proposed Solution

### Two weights: `Weight` (real) and `CapWeight` (trust)

We keep the existing `Weight` field as the **real** weight and add a new `CapWeight` field as the **trust** weight.

- **`Weight`** — the real, fully-adjusted weight (after penalties, collateral, and the universal 30% concentration cap). It remains the single source of truth for:
  - **Rewards** (settlement uses `Weight * confirmed / rawConfirmationTotal`).
  - **cPoC confirmation** (checking whether a participant confirmed its claimed weight).
  - **The cap baseline** for the *next* epoch (what a participant proved this epoch).
  - `getEffectiveValidationBaseState`, root `ValidationWeight.Weight`, unit-of-compute pricing, and weighted random selection.

- **`CapWeight`** — the trust weight, equal to `Weight` by default but capped at the participant's previous-epoch confirmed weight (and `0` for participants absent last epoch). It is the value used by:
  - **Governance / validator power** (CometBFT `ValidatorUpdate` via `SetComputeValidators`).
  - **BLS threshold signing** (percentage/slot assignment).
  - **cPoC validation voting power** (per-model voting powers).

Keeping `Weight` real (rather than capping it and adding an "uncapped" field for rewards) is deliberate: `Weight` is read in many places that require the *real* value — rewards, cPoC confirmation, and the cap baseline itself. Capping `Weight` would have silently corrupted all of those. Adding `CapWeight` as the new, explicitly-routed value keeps every existing reader of `Weight` correct and requires no upgrade fallback for the reward/settlement path.

### The cap value (cPoC / model-coefficient aware)

The cap for each participant is the confirmed **effective weight** they held in the previous epoch, computed identically to the settlement/reward path:

```
cap = previousWeight * previousConfirmationWeight / previousRawConfirmationTotal
```

where `previousConfirmationWeight` and `previousRawConfirmationTotal` are aggregated across the per-model subgroups using the same confirmation-weight scale coefficients used for rewards. This guarantees the cap reflects exactly what the participant *confirmed* last epoch, honoring model coefficients and cPoC adjustments. When no confirmation scales are configured, the cap degrades to the previous consensus weight itself.

This logic is centralized in a shared helper, `types.EffectiveConfirmedWeight`, used by both the reward path and the cap so the two can never drift apart.

### New / returning participants

A participant that was **not present** in the previous epoch has no confirmed weight to cap against, so its `CapWeight` is set to `0`:

- It earns rewards normally on its real `Weight`.
- It has **zero** governance power (dropped from the validator set), **zero** BLS share, and **zero** cPoC validation voting power for this first epoch.
- Next epoch, its real `Weight` from this epoch becomes its cap baseline, so it can then take on trust weight up to what it just proved.

### Pipeline placement

`CapWeight` is computed at end-of-PoC-validation, in this order:

1. Penalties applied.
2. Collateral adjustment.
3. Universal 30% power cap (`applyEpochPowerCapping`) — still applied to real `Weight`.
4. **`applyPreviousConfirmedWeightCap`** — default `CapWeight = Weight` for everyone, then lower it to the previous-epoch confirmed cap (or `0` for new participants).
5. Per-model voting powers computed from `CapWeight`.
6. `ActiveParticipants` persisted (including `CapWeight`).
7. Epoch members added (x/group weights remain real `Weight`).
8. BLS key generation initiated using `CapWeight`.

Governance validator power is applied a couple of blocks later at `SetComputeValidators`; it reads the persisted `CapWeight` and caps/drops validators accordingly, while the x/group member weights stay real so rewards, pricing, and weighted selection are unaffected.

## Consumers affected

| Consumer | Weight used | Behavior |
| --- | --- | --- |
| Rewards / settlement | `Weight` (real) | Unchanged — full reward on proven weight |
| cPoC confirmation | `Weight` (real) | Unchanged |
| Next-epoch cap baseline | `Weight` (real) | Unchanged |
| Unit-of-compute pricing | `Weight` (real) | Unchanged |
| Weighted random selection | `Weight` (real) | Unchanged |
| Governance / CometBFT power | `CapWeight` | **Capped**, new participants dropped |
| BLS threshold signing | `CapWeight` | **Capped**, new participants get 0 slots |
| cPoC validation voting power | `CapWeight` | **Capped**, new participants get 0 voting power |

## Edge cases and upgrade safety

- **Genesis / bootstrap**: If there is no effective epoch yet, capping is skipped and `CapWeight` defaults to `Weight`, so the initial validator set is never zeroed.
- **Missing previous epoch group data**: Capping is skipped (defaults to `Weight`) rather than zeroing everyone.
- **Upgrade transition**: An epoch formed *before* this change has no `CapWeight` populated (all zero). Both the governance cap and the shared trust-weight resolver detect the "cap not applied" state (no participant has a positive `CapWeight`) and fall back to real `Weight`, so the transition epoch never collapses to an all-zero validator set.
- **All-new epoch (degenerate)**: Same fallback applies, avoiding a chain halt.

Because `Weight` stays real and always-populated, the reward and settlement paths need **no** migration or fallback.

## Implementation

New field:

- `ActiveParticipant.cap_weight` (proto field 9) in `inference-chain/proto/inference/inference/activeparticipants.proto`, regenerated into `activeparticipants.pb.go` / `activeparticipants.pulsar.go`.

Core logic — `inference-chain/x/inference/module/previous_epoch_cap.go`:

- `applyPreviousConfirmedWeightCap` — computes `CapWeight` per participant.
- `buildPreviousConfirmedWeightCaps` — builds the per-address cPoC-aware cap map from previous epoch data.
- `capComputeResultsToPreviousConfirmedWeight` — caps/drops validators at `SetComputeValidators` time.
- `resolveTrustWeights` — shared resolver returning `CapWeight` when applied, else `Weight`.

Shared helper — `inference-chain/x/inference/types/weight.go`:

- `EffectiveConfirmedWeight(weight, confirmationWeight, rawConfirmationTotal)` — single source of truth for confirmed effective weight, used by both rewards and the cap.

Wiring:

- `inference-chain/x/inference/module/module.go` — pipeline order, governance cap at `SetComputeValidators`, BLS via `CapWeight`.
- `inference-chain/x/inference/module/delegation_pipeline.go` — voting powers via `CapWeight`.
- `inference-chain/x/inference/module/genesis_guardian_enhancement.go` — BLS guardian slot reservation via `CapWeight`.
- `inference-chain/x/inference/keeper/bitcoin_rewards.go` — refactored to use the shared `EffectiveConfirmedWeight` helper (behavior-identical).

## Testing

- `types.EffectiveConfirmedWeight` — unit tests covering full/partial/zero confirmation, truncation, clamping, and negative inputs.
- `applyPreviousConfirmedWeightCap` — clamps over-weight participants, zeroes new participants, preserves real `Weight`, and skips on bootstrap / missing previous group.
- `resolveTrustWeights` — uses `CapWeight` when applied; falls back to `Weight` when unset.
- `capComputeResultsToPreviousConfirmedWeight` — clamps and drops validators; falls back when `CapWeight` is unset (upgrade transition).

All `x/inference` and `x/bls` test suites pass.
