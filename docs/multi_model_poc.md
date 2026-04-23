# Multi-Model PoC — Host Operations Guide

This document describes **operator-facing** behavior and **CLI commands** for the multi-model Proof-of-Compute (PoC) system introduced in Upgrade v0.2.12.

**Scope:** `inferenced` queries and transactions hosts care about — delegation, PoC v2 visibility, and hardware registration. PoC v2 **submission** (store commits, ML-node weight distribution, validation batches) is performed by **decentralized-api (DAPI)** in normal operation; this guide references those messages only for context.

For design background see [proposals/multi-model-poc/README.md](../proposals/multi-model-poc/README.md) and the two design docs in that directory. For single-model PoC mechanics see [gonka_poc.md](gonka_poc.md). For the v0.2.12 fee changes see [host_onboarding.md](host_onboarding.md).

---

## Concepts

- **Per-model PoC groups.** Each governance-approved model has its own PoC group. PoC for different models runs **in parallel** within the same epoch, keyed by `model_id` end-to-end in state (`PocParams.models[]`, commits, validations, weight distribution).
- **Two weight notions.**
  - **Model-local PoC weight** — raw work proven inside a model group (used for routing and inference incentives within that group).
  - **Consensus weight** — aggregated across eligible model groups as `Σ weight_scale_factor_i × pocWeight_i`, with caps applied. Drives block signing, governance vote, PoC-validation voting, and Bitcoin-style rewards.
- **Delegation.** If you do **not** serve model `X`, you can delegate your consensus weight to a direct member of group `X` so they vote on PoC validation on your behalf. Without a valid delegation, non-members of `X` effectively **abstain** for that model's validation.

The [Participant modes](#participant-modes) and [Penalties & weight adjustment](#penalties--weight-adjustment) sections spell out the behavior in detail.

---

## Participant modes

Modes are evaluated **per governance-approved eligible model** and **per participant** when the chain forms the next epoch's participation state.

- **Regular delegation snapshot** is captured at `poc_validation_start` (see `AppModule.BeginBlocker` in `inference-chain/x/inference/module/module.go` — `IsStartOfPoCValidationStage` branch). This snapshot contains **delegations and refusals only**; direct intents are **intentionally excluded** and handled by the bootstrap snapshot (see the `DelegationSnapshot` message in `inference-chain/proto/inference/inference/poc_delegation.proto`).
- **Bootstrap delegation snapshot** is captured earlier at `start_poc − deploy_window` blocks (see `IsDelegationSnapshotHeight`). This snapshot contains delegations and **intents**, used only for pre-eligibility of not-yet-active models.
- **Direct membership** is inferred at resolution time from **actual PoC v2 store commits** in that epoch — there is no separate "direct" transaction.

The `ParticipationMode` enum for the regular (eligible-model) calculation has **four** values (see `inference-chain/x/inference/module/delegation_weight_calculator.go`): **DIRECT, REFUSE, DELEGATE, NONE**. Bootstrap penalty modes for not-yet-eligible models are a separate enum (`BootstrapPenaltyMode` in `bootstrap_penalty.go`) with values **BootstrapPenaltyDirect, BootstrapPenaltyDelegate, BootstrapPenaltyIntentOK, BootstrapPenaltyIntentMissed, BootstrapPenaltyNone**.

### DIRECT

- **Meaning.** The participant submitted a valid `MsgPoCV2StoreCommit` with an entry for `model_id` for the relevant PoC stage. Direct membership is **inferred** from the presence of that commit — it is not a separate transaction.
- **Precedence.** If the participant also has a DELEGATE/REFUSE/INTENT record for the same `(model_id, participant)`, **DIRECT wins** for the epoch and the other record is ignored by the voting-power calculator.
- **Penalties.** DIRECT is **not** charged the refusal or no-participation fractions for that model (see `delegation_weight_adjustment.go`, the `ModeDirect` branch returns immediately).

### DELEGATE

- **Meaning.** The participant broadcast `MsgSetPoCDelegation` with a non-empty `delegate_to` for `model_id`, and at resolution time:
  - The target is a **direct member** of that model's group (i.e. the target itself submitted a store commit for `model_id` in this epoch), **and**
  - The target has **positive N−1 consensus weight**.
- **If the target is not a member or has zero weight**, the delegator falls through to **NONE** for that model.
- **Effect.** The delegator's consensus weight (full amount, not `delegation_share`) is added to the target's **voting power** for that model group, used for PoC-validation acceptance. The weight-adjustment layer separately transfers `delegation_share × originalWeight` from the delegator to the target in consensus space (see [Penalties](#penalties--weight-adjustment)).
- **Tx.** `inferenced tx inference set-poc-delegation <model-id> <delegate-to>`. Pass an empty `delegate_to` to clear.

### REFUSE

- **Meaning.** The participant broadcast `MsgRefusePoCDelegation` for `model_id`, opting out of delegating for that model.
- **Effect.** No voting power is passed to anyone for that model. The participant is not a direct member unless they also have a commit (in which case DIRECT wins).
- **Penalties.** After `penalty_start_epoch` for that model, the participant may be charged `refusal_penalty` on **original** consensus weight.

### INTENT (bootstrap-only)

- **Meaning.** The participant broadcast `MsgDeclarePoCIntent` for a governance-approved model that is **not yet in the consensus-eligible set** — a signal of intent to deploy hardware in time for PoC.
- **When it matters.** Only inside the bootstrap snapshot, captured at `start_poc − deploy_window`. Used to compute the `BootstrapModelPreEligibility` report per model.
- **INTENT does not make you DIRECT.** At resolution time only an actual PoC store commit makes you DIRECT.
- **Ignored for already-eligible models.** Once a model is in the eligible set, its `ParticipationMode` resolution uses the regular four-mode enum; INTENT records for eligible models have no effect on voting power or penalties.
- **Penalties (bootstrap-specific).** After `penalty_start_epoch`, `BootstrapPenaltyIntentMissed` (intent declared but no commit by the model's PoC) and `BootstrapPenaltyNone` (no delegation, intent, or commit) incur `no_participation_penalty`. `BootstrapPenaltyDelegate` and `BootstrapPenaltyIntentOK` (intent on a not-yet-eligible model that isn't expected to commit yet) are **not** penalized. See `AccumulateBootstrapPenalties` in `bootstrap_penalty.go`.

### NONE

- **Meaning.** For that eligible model, the participant is not DIRECT, has no valid delegation (including delegation to a non-member or zero-weight target), and has not recorded a refusal.
- **Effect.** No voting power for that model; effectively abstains on PoC validation for that group.
- **Penalties.** After `penalty_start_epoch`, incurs `no_participation_penalty` for that model.

### Exclusivity

The three delegation transactions (`MsgSetPoCDelegation`, `MsgRefusePoCDelegation`, `MsgDeclarePoCIntent`) are mutually exclusive per `(model_id, participant)` at the state layer. Each handler in `inference-chain/x/inference/keeper/msg_server_poc_delegation.go` explicitly clears the other two records (via `ClearOtherDelegationState`) before writing its own — **last write wins**. DIRECT is not a state record and overrides whichever of the three happens to be present, for that epoch only.

---

## Penalties & weight adjustment

Penalties and the `delegation_share` transfer apply to **consensus weight** during epoch formation, after PoC results are known. The accumulator lives in `inference-chain/x/inference/module/delegation_weight_adjustment.go`.

### Parameter sources

| Source | Field | Role |
|---|---|---|
| `PocParams.models[<i>]` | `penalty_start_epoch` | Before this **epoch index**, no refusal / no-participation / delegation-share fractions are applied for that model. Per-model, so different models can phase in penalties at different times. |
| `PocParams.models[<i>]` | `weight_scale_factor` | Coefficient converting model-local PoC weight to consensus weight. Governance-set per model. |
| `DelegationParams` | `refusal_penalty` | Decimal fraction of **original** consensus weight deducted when mode is REFUSE (after `penalty_start_epoch`). |
| `DelegationParams` | `no_participation_penalty` | Fraction deducted when mode is NONE (regular resolution) or one of the penalized bootstrap modes. |
| `DelegationParams` | `delegation_share` | Fraction of the **delegator's original** weight transferred to the delegate target when mode is DELEGATE. This is a **reallocation**, not additive with the two penalties above. |
| `DelegationParams` | `deploy_window` | Blocks before `start_poc` used as the bootstrap snapshot cutoff. |
| `DelegationParams` | `w_threshold` | Minimum fraction of total network consensus weight from members for a group to be pre-eligible. |
| `DelegationParams` | `v_min` | Minimum number of hosts with non-zero consensus weight required in a group. |
| `DelegationParams` | `cap_factor` | Cap on a group's consensus-weight contribution relative to members' weight in other groups. |
| `DelegationParams` | `initial_model_id` | Model ID exempt from the group cap (set during migration to the founding model). |
| `DelegationParams` | `max_model_voting_power_percentage` | Max fraction of per-model voting power any single host can hold **after** delegation is resolved. Zero disables the cap. Applied per-model, excess is burned (not redistributed). |

Query everything in one call:

```bash
inferenced query inference params --node "$NODE" -o json
```

### Regular-model penalty resolution

For each **eligible** model and each participant with positive N−1 consensus weight, `ResolveGroupParticipation` assigns a mode. Then, if `upcoming_epoch_index ≥ penalty_start_epoch` for that model:

- **DIRECT** → no fractions charged (the `ModeDirect` branch in `PenaltyAccumulator` skips the participant).
- **REFUSE** → add `refusal_penalty` to this participant's accumulated fraction.
- **NONE** → add `no_participation_penalty`.
- **DELEGATE** → schedule a weight transfer of `delegation_share × originalWeight` from delegator to target; clamped so the delegator's remaining weight does not go negative.

### Bootstrap penalty resolution

Handled by `AccumulateBootstrapPenalties`. Eligible groups skip this path entirely. For **not-yet-eligible** models past `penalty_start_epoch`, `BootstrapPenaltyIntentMissed` and `BootstrapPenaltyNone` each add `no_participation_penalty`. `BootstrapPenaltyDirect`, `BootstrapPenaltyDelegate`, and `BootstrapPenaltyIntentOK` add nothing.

### Caps

Penalty fractions from all models are accumulated per participant and **capped at 1.0** before being applied (`PenaltyAccumulator.Apply` at the `totalFrac.GT(one)` check): a participant cannot lose more than 100% of their original pre-adjustment weight from the fraction mechanism.

### Pipeline order

From `inference-chain/x/inference/module/module.go` (`onEndOfPoCValidationStage`):

1. **Consensus weight computation** with per-group caps from `cap_factor` and `initial_model_id`.
2. **Penalty accumulator apply**: deduct capped penalty fractions first, then apply `delegation_share` transfers. Both run together.
3. **Collateral adjustment** (`AdjustWeightsByCollateral`): may further reduce weights per collateral status.
4. **Universal power capping** (`applyEpochPowerCapping`): network-wide voting-power cap.
5. **Per-model voting-power cap** inside `computeAndSetVotingPowers`: clips any host above `max_model_voting_power_percentage` for that model; excess is burned, not redistributed.

### Practical notes

- If **all** of `refusal_penalty`, `no_participation_penalty`, and `delegation_share` are zero, the accumulator is a no-op (`DelegationAdjustmentParams.IsNoOp`). You can keep multi-model wiring hot with penalties disabled by leaving these at zero and only setting them via governance once hosts have had time to configure their delegation preferences.
- `penalty_start_epoch` is per-model — track it per `model_id`, not globally.
- Bootstrap pre-eligibility is surfaced via the `bootstrap_model_preeligibility` event (emitted per model from `emitBootstrapPreEligibilityEvents` in `delegation_pipeline.go`). Attributes include `model_id`, `pre_eligible`, `meets_weight_threshold`, `meets_v_min`, `meets_reachability`, `intent_host_count`, `intent_weight`, `reachable_voting_power`, `total_network_weight`, and `snapshot_height`. Subscribe to these before committing hardware to a new model.

---

## Prerequisites

Chain binary: `inferenced`.

Example environment:

```bash
export NODE="tcp://127.0.0.1:26657"   # or your RPC endpoint
export CHAIN_ID="gonka-mainnet"
export KEY="mykey"                    # key name in your keyring
```

---

## Queries

All queries are registered in `inference-chain/x/inference/module/autocli.go`. Names and argument order below are copied from there; run each with `--help` to see flags.

### Module parameters

Includes `poc_params` (with `models[]`), `delegation_params`, and fee params.

```bash
inferenced query inference params --node "$NODE" -o json
```

### Governance-approved models

```bash
inferenced query inference models-all --node "$NODE"
```

### Current epoch

```bash
inferenced query inference get-current-epoch --node "$NODE"
```

### PoC delegation state for a participant

The second positional arg (`model-id`) is **optional**. Omit it (or pass an empty string) to get all models for the participant.

All models:

```bash
inferenced query inference poc-delegation <participant-address> --node "$NODE"
```

Single model:

```bash
inferenced query inference poc-delegation <participant-address> <model-id> --node "$NODE"
```

The response carries three repeated fields: `delegations`, `refusals`, `intents`. Because the three transaction types are mutually exclusive per `(model_id, participant)`, at most one of these will contain a record for a given model.

### PoC v2 store commits for a PoC stage

`<poc-stage-start-block-height>` is the **start block of that PoC stage** — the epoch's PoC window start. These queries exist for operator diagnostics; DAPI submits the commits.

All commits for the stage:

```bash
inferenced query inference all-poc-v2-store-commits <poc-stage-start-block-height> --node "$NODE"
```

Specific participant:

```bash
inferenced query inference poc-v2-store-commit <poc-stage-start-block-height> <participant-address> --node "$NODE"
```

### ML-node weight distribution (PoC v2)

```bash
inferenced query inference mlnode-weight-distribution <poc-stage-start-block-height> <participant-address> --node "$NODE"
```

---

## Transactions — delegation and intent

The three delegation messages use `sender` as the signer field (not `creator`) — see `inference-chain/proto/inference/inference/poc_delegation.proto`. Message handlers are in `inference-chain/x/inference/keeper/msg_server_poc_delegation.go`; each validates the `model_id` against the governance-approved model set (`GetGovernanceModel`) and, for `SetPoCDelegation`, validates that `delegate_to` resolves to a known participant.

The target does **not** have to be a direct member of the group at the time you set the delegation. Membership is evaluated later, at the regular snapshot (`poc_validation_start`), against actual PoC v2 store commits. If the target ends up not being a direct member or has zero prior-epoch consensus weight when the snapshot is resolved, your delegation falls through to NONE for that epoch and a `no_participation_penalty` may apply after `penalty_start_epoch`.

Standard flags used in the examples below:

```bash
TX_FLAGS=(--from "$KEY" --node "$NODE" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.3 -y)
```

### Set PoC delegation

Delegate your consensus weight for `<model-id>` to `<delegate-to>`:

```bash
inferenced tx inference set-poc-delegation "<model-id>" "<delegate-to>" "${TX_FLAGS[@]}"
```

Clear delegation (empty `delegate_to`):

```bash
inferenced tx inference set-poc-delegation "<model-id>" "" "${TX_FLAGS[@]}"
```

### Refuse PoC delegation

Explicitly refuse to participate for `<model-id>`. After that model's `penalty_start_epoch`, `refusal_penalty` may apply on consensus weight.

```bash
inferenced tx inference refuse-poc-delegation "<model-id>" "${TX_FLAGS[@]}"
```

### Declare PoC intent (bootstrap)

For a governance-approved model that is **not yet in the eligible set**, signal that you intend to deploy hardware before its PoC starts. Only meaningful until the model becomes eligible; ignored afterward.

```bash
inferenced tx inference declare-poc-intent "<model-id>" "${TX_FLAGS[@]}"
```

### Reminder on mutual exclusion

Sending any of the three above clears the other two for the same `(model_id, sender)` — last write wins. DIRECT (derived from a store commit in the current epoch) overrides all three for resolution purposes.

---

## Hardware registration

Register or update ML-node hardware:

```bash
inferenced tx inference submit-hardware-diff "${TX_FLAGS[@]}"
```

The message takes structured hardware fields; run `inferenced tx inference submit-hardware-diff --help` for the current flag set. Flags depend on your version.

---

## PoC v2 chain messages (reference)

PoC v2 uses three **batched** messages per PoC stage, all keyed by `poc_stage_start_block_height` and `model_id`:

- **`MsgPoCV2StoreCommit`** — `entries[]` of `PoCV2CommitEntry{ model_id, count, root_hash }`. Stored keyed by `(stage, participant, model_id)` so different models do not collide. Presence of an entry for a given `model_id` is what makes the participant DIRECT for that model.
- **`MsgMLNodeWeightDistribution`** — `entries[]` of `MLNodeDistributionEntry{ model_id, weights[] }`, where each `MLNodeWeight` is a `{node_id, weight}`.
- **`MsgSubmitPocValidationsV2`** — `validations[]` of `PoCValidationEntryV2{ participant_address, validated_weight, model_id }`; each entry carries its own `model_id`.

All three messages use `creator` as the signer field (not `sender`). The proto definitions live in `inference-chain/proto/inference/inference/tx.proto` (messages) and `inference-chain/proto/inference/inference/poc_v2.proto` (entry types).

In normal operation **DAPI** builds and broadcasts these from its off-chain artifact store — operators do not submit them manually. The `count`/`root_hash` fields on `MsgPoCV2StoreCommit` and the flat `weights` field on `MsgMLNodeWeightDistribution` are deprecated single-model holdovers; new code writes only `entries[]`. These three message types are not explicitly wired in `autocli.go`; treat DAPI as the only supported submission path.

---

## Operator checklist

1. **One logical model per ML node** in configuration where practical. Multi-model node assignment is deterministic but easy to misconfigure; review node configs after upgrading.
2. Before `penalty_start_epoch` for each model you care about, pick **DELEGATE**, **REFUSE**, or accept implicit **NONE**. Query `inference params` to confirm `DelegationParams` fractions are non-zero (zero = penalties disabled) and each model's `penalty_start_epoch`.
3. Subscribe to `bootstrap_model_preeligibility` events for any model you are considering. Use `MsgDeclarePoCIntent` before `start_poc − deploy_window` if you plan to deploy; actually submit a store commit via DAPI during PoC to become DIRECT.
4. After submitting `MsgSetPoCDelegation`, verify with `inferenced query inference poc-delegation <your-address> <model-id>`. Nothing prevents delegating to a target that is not yet a member — validity is checked at snapshot time.
5. After upgrades, confirm with `inferenced query inference params` that `poc_params.models[]` contains every model you expect and that `delegation_params` is configured. All-zero `DelegationParams` fractions disable penalties and transfers.
