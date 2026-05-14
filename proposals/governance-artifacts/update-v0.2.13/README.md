# Upgrade Proposal: v0.2.13

This proposal covers the v0.2.13 microrelease.

The headline change is the addition of `MiniMaxAI/MiniMax-M2.7` as a third
governance-approved inference model, paired with a coefficient rebalance
across all three models (MiniMax newly added, Kimi K2.6 reduced, Qwen3-235B
unchanged) and per-model `validation_threshold` updates.

The release also fixes confirmation PoC reward accounting, devshard escrow
params, complaint-response authz grants, upstream response parsing,
participant reactivation, node-manager gRPC defaults, and devshard storage
growth. The upgrade also disables confirmation PoC for the rest of the
upgrade epoch so the new snapshot logic starts cleanly from the next epoch.

## Upgrade Plan

The node binary is upgraded through an on-chain software upgrade proposal.

The PR also updates `api` and `node` container versions in
`deploy/join/docker-compose.yml` for hosts joining after the on-chain upgrade.

Existing hosts are not required to manually update their `api` or `node`
containers as part of the chain upgrade.

Hosts who plan to serve `MiniMaxAI/MiniMax-M2.7` must additionally prepare
storage and bandwidth for the model weights (~230 GB FP8) before the model
becomes active. See "Action will be required" below.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

## Testing

The on-chain upgrade from `v0.2.12` to `v0.2.13` is being validated on testnet
under `release/v0.2.13-testnet-rehearsal-2` and `release/v0.2.13-testnet2`.
Reviewers are encouraged to request access to testnet environments to validate
node behavior and the on-chain upgrade process, or to replay the upgrade on
private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.13/inference-chain/app/upgrades/v0_2_13/upgrades.go).

Migrations:

- Adds `MiniMaxAI/MiniMax-M2.7` as a governance-approved model and registers
  its `PoCModelConfig` (`seq_len = 1024`, `weight_scale_factor = 0.3024`,
  `penalty_start_epoch = 271`). See "New governance model: MiniMax-M2.7"
  below.
- Lowers Kimi K2.6 `weight_scale_factor` from `1.26` to `0.78` (≈38%
  reduction). See "Kimi K2.6 coefficient adjustment" below.
- Updates per-model `validation_threshold` values: Qwen3-235B set to `0.940`
  (newly populated), Kimi K2.6 lowered from `0.920` to `0.900`, MiniMax-M2.7
  set to `0.922`. See "Validation threshold updates" below.
- Adds `--enable-auto-tool-choice` to Kimi K2.6's `Model.ModelArgs`. The
  model was deployed in v0.2.12 without this flag, which caused some tool
  calls to be missed; this migration prepends the flag idempotently.
- Sets `DevshardEscrowParams.MaxEscrowsPerEpoch` to `500_000`.
- Sets `DevshardEscrowParams.MaxNonce` to `1_000_000`. The previous settlement
  path used a hardcoded `20_000` nonce limit.
- Backfills `EpochGroupData.ConfirmationWeightScales` for the current epoch and
  clamps existing confirmation weights down to the new expected value.
- Backfills `MsgRespondDealerComplaints` authz grants on existing cold-to-warm
  ML ops pairs. v0.2.12 added this message to the permission list but did not
  migrate existing grants, so DAPIs that joined before v0.2.12 could not respond
  to dealer complaints.
- Disables confirmation PoC triggers for the rest of the upgrade epoch via a
  grace-epoch `UpgradeProtectionWindow` of 3000 blocks. The new snapshot logic
  starts from the next epoch.

## Changes

### New governance model: MiniMax-M2.7 FP8 (`MiniMaxAI/MiniMax-M2.7`)

The upgrade introduces `MiniMaxAI/MiniMax-M2.7` (FP8 quantization) as a third
governance-approved inference model. The migration registers both the
governance `Model` entry (HF repo, commit, tool/reasoning parsers, VRAM and
throughput hints) and the corresponding `PoCModelConfig` (`seq_len = 1024`,
validation threshold `0.922`).

**Coefficient calibration.** MiniMax-M2.7 is set to `0.3024`. In the same
upgrade, Kimi K2.6 is reduced from `1.26` to `0.78` (see "Kimi K2.6
coefficient adjustment"). Qwen3-235B stays at `0.359`. Post-upgrade
per-8xGPU consensus output:

| GPU (8xGPU) | MiniMax-M2.7 (new) | Kimi K2.6 (new) | Qwen3-235B (unchanged, vLLM 0.20.0) |
|---|---:|---:|---:|
| A100 80GB | 542 | n/a | n/a |
| H100 | **1432** | 542 | 1382 |
| H200 | **2090** | 949 | ~1989 |
| B200 | 3174 | **3494** | 3177 |
| B300 | ~3629 | **3994** | 3676 |

The calibration makes MiniMax-M2.7 the clearly highest-paying choice on
H100 / H200, and leaves Kimi narrowly ahead on B200 / B300 (only ~10%
over MiniMax and Qwen, vs. the prior ~2× advantage). A100 80GB owners are
*newly eligible* to earn consensus weight via MiniMax — Kimi/Qwen could
not be served on that hardware tier.

**Bootstrap grace.** `penalty_start_epoch` is hardcoded to `271`. This is
chosen to give operators a predictable activation date independent of
when the upgrade actually executes on chain. At the current mainnet block
time of ~5.13 sec/block × 17,280 blocks per epoch ≈ 24.6 hours per epoch,
epoch 271 will land ~8 days after the upgrade lands at the current
mainnet epoch (~263 as of doc writing). This is longer than the Kimi
v0.2.12 precedent (`current_epoch + 3`, see
[`kimiPenaltyStartEpoch`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.12/inference-chain/app/upgrades/v0_2_12/upgrades.go#L592-L600)).
The extra runway accommodates the ~230 GB FP8 weights download and the
broader operator pool (A100 80GB owners become newly eligible and may not
have flagship-model deployment muscle memory).

**Pre-eligibility.** MiniMax follows the standard multi-model bootstrap
flow introduced in v0.2.12:

1. A `BootstrapDelegationSnapshot` is captured at `start_poc - deploy_window`
   (`DeployWindow = 500` blocks, ~43 minutes). Hosts that have declared
   INTENT or DELEGATE prior to this point are recorded in the snapshot.
2. Pre-eligibility is evaluated against `V_min = 3` DIRECT committers,
   `W_threshold = 0.3` of total weight, and `>2/3` reachability from the
   INTENT + DELEGATE set. Events are emitted so operators see viability
   before committing hardware.
3. At PoC start, hosts that actually submitted store commits resolve as
   DIRECT; others resolve as DELEGATE / REFUSE / INTENT / NONE based on
   their tx records.
4. Once `current_epoch >= penalty_start_epoch` (271), NONE / IntentMissed
   costs `NoParticipationPenalty = 0.15` per epoch per missed model.

**Quorum parameters unchanged.** `V_min`, `W_threshold`, `CapFactor`,
`MaxModelVotingPowerPercentage`, and the penalty fractions are unchanged
from v0.2.12. The values used to bootstrap Kimi are sufficient for
MiniMax bootstrap.

### Kimi K2.6 coefficient adjustment

Kimi K2.6 `weight_scale_factor` is reduced from `1.26` to `0.78` (a ~38%
reduction in Kimi-derived consensus weight at the same hardware). The
adjustment is intended to flatten the cross-model consensus output on
B200/B300 flagship hardware, so that:

- H100/H200 owners face a clear signal to migrate from Kimi (or Qwen)
  to MiniMax-M2.7, where their hardware earns 2-3× the Kimi-on-H100
  consensus output.
- B200/B300 owners face a tighter cross-model tradeoff (Kimi narrowly
  ahead, with Qwen and MiniMax within ~10%). The choice becomes more
  driven by inference demand and operational preference rather than by
  raw coefficient.

This is a meaningful change for current Kimi operators on flagship
hardware: per-epoch consensus weight from Kimi drops by ~38% holding
PoC weight constant. Operators are expected to either accept the new
rate or migrate to MiniMax (recommended on H100/H200) or Qwen (modest
impact on B200/B300).

**Qwen3-235B coefficient is unchanged at `0.359`.** Per the cross-model
direction, Qwen3-235B is expected to fade from the network organically
as operators migrate to the better-fitting models above. The chain does
not need to take an explicit action against Qwen.

### Validation threshold updates

The per-model `Model.ValidationThreshold` (used by inference-result
cross-validation, not by PoC) is updated for all three models:

- **Qwen3-235B**: set to `0.940` (newly populated; was previously default).
- **Kimi K2.6**: lowered from `0.920` to `0.900`.
- **MiniMax-M2.7**: set to `0.922` (initial value for the new model).

These reflect measured per-model agreement rates from production
inference traffic. A lower threshold means a host's inference output
needs to match the validator's recomputation slightly less strictly to
avoid invalidation. The values are calibrated independently per model
because models exhibit different intrinsic numerical-determinism
characteristics on the same input.

### Devshard escrow parameters

`DevshardEscrowParams.MaxEscrowsPerEpoch` is raised to `500_000` and
`MaxNonce` to `1_000_000`. These reflect observed production volumes after
the v0.2.12 devshard rollout and unblock further growth without a follow-up
upgrade. Devshard settlement now reads the nonce limit from
`DevshardEscrowParams.MaxNonce` instead of a hardcoded constant.

### Confirmation weight scales

Confirmation PoC used different model sets for measured weight, preserved
weight, and reward rescaling. During new-model bootstrap, this could reduce
confirmation weight for honest miners serving both an eligible model and a
not-yet-eligible model. v0.2.13 stores one epoch snapshot of confirmable
models and weight-scale factors, then uses it for confirmation and reward
calculations.

A new precomputed `ConfirmationWeightScales` collection is materialized on
the root `EpochGroupData` so per-model `weight_scale_factor` is observable
per epoch. Existing `ConfirmationWeight` values are clamped to the new
expected value during migration to prevent any retroactive inflation.
User-visible weight semantics are unchanged.

### Other inference-chain fixes

- `ConsecutiveInvalidInferences` was not reset when a participant became
  ACTIVE again. A host could return from invalid state and be invalidated
  again after one new failure. v0.2.13 resets the counter on reactivation
  and upcoming promotion.

### decentralized-api

- Some OpenAI-compatible upstreams return numeric `stop_reason` values.
  `Choice.StopReason` now accepts any JSON type, so those responses no
  longer fail unmarshalling.
- `NodeManagerGrpcPort` did not start by default when unset. It now
  defaults to `9400`, and join compose uses the same default so devshard
  can reach the API without manual config.
- The internal devshard service inside dapi uses the same devshard storage
  changes listed below, including pruning and Postgres support.

### devshard

- Devshard storage could grow forever because old escrow data stayed in
  one SQLite store. Storage is now epoch-scoped and prunes old epochs in
  the background, keeping the latest 3 epochs.
- Devshard can use Postgres as the primary store for larger deployments,
  with SQLite kept as a local fallback.
- Postgres data is partitioned by `epoch_id` for sessions, diffs, and
  signatures, so pruning can drop old epoch data cleanly.

## Action will be required

### All hosts: declare your MiniMax participation mode by epoch 271

Penalty enforcement for MiniMax begins at chain epoch `271`. Each host
must choose one participation mode for MiniMax-M2.7 by then. Hosts who
do nothing will take a 15% consensus-weight penalty per epoch from that
point on, applied additively against other model penalties (capped at
100%).

- **DIRECT**: deploy MiniMax-M2.7 on capable hardware (A100 80GB+) and
  submit PoC commits during the model's PoC stage. The model becomes one
  of your active inference targets.
- **DELEGATE**: delegate your PoC validation power for MiniMax to another
  host that *is* running it. Costs `DelegationShare = 0.05` of your
  weight transferred to the delegate. No hardware deployment required.
- **REFUSE**: explicitly opt out. Costs `RefusalPenalty = 0.10` per epoch.
  Use this if you have a deliberate reason not to participate (e.g.
  contractual restrictions).
- **INTENT**: declare intent to participate before deploying. Protected
  from penalty *only if the model fails to bootstrap* (does not reach
  pre-eligibility). Once MiniMax becomes pre-eligible, INTENT-without-
  commit converts to `IntentMissed` and incurs the standard penalty.

### A100 80GB owners specifically

If you run 8xA100 80GB hardware (or larger A100 configurations),
MiniMax-M2.7 is the first governance-approved model that fits your VRAM
envelope. Kimi K2.6 and Qwen3-235B require larger memory than A100 80GB
provides. Deploying MiniMax makes A100 owners newly eligible for consensus
weight rewards.

Approximate consensus output: 8×A100 80GB running MiniMax-M2.7 produces
~542 consensus per minute (~26% of an 8×H200 running MiniMax).

### H100 / H200 owners

MiniMax-M2.7 is the highest-paying model choice on your hardware
(~1432 / ~2090 consensus/min on 8xGPU respectively). Migrating from Qwen
or Kimi to MiniMax on H100/H200 is the network-preferred direction.

Note that switching models incurs a one-time cost: 230 GB FP8 weights
download plus model-load warmup. Plan to start the download as soon as
the upgrade activates (or earlier if you have advance notice).

### B200 / B300 owners

No change required. Kimi K2.6 remains the highest-paying model choice on
flagship B200/B300 hardware (~3494 / ~3994 consensus/min on 8xGPU
respectively, after the Kimi coefficient reduction). Continue running
Kimi if that's your current setup.

### Dashboard maintainers

Dashboards that follow the per-model summation approach documented in
[`dashboard-weight-computation.md`](https://github.com/gonka-ai/gonka/blob/main/docs/dashboard-weight-computation.md)
require no changes. They iterate over `epoch_group_data.sub_group_models[]`
and apply per-model coefficients from `params.poc_params.models[]`
dynamically, so MiniMax appears automatically once the chain returns it
in the sub-group list.

Dashboards that have hardcoded the set of models (e.g. show Qwen + Kimi
explicitly) will silently omit MiniMax until updated. If your dashboard
falls into this category, replace the hardcoded model list with
`params.poc_params.models[].model_id`.

No metric formulas change in v0.2.13. The `weight` /
`weight_to_confirm` / `confirmation_weight` / `confirmation_ratio`
definitions are the same as documented after the v0.2.12 multi-model PoC
rollout.

## Outstanding items before activation

The upgrade handler logic is finalized on the `gm/minimax` branch.
Remaining blockers before the on-chain upgrade proposal lands on
mainnet:

1. **PoC reference artifact for MiniMax-M2.7**: a 200-nonce vector file
   recorded on the reference rig (B200, tp=4, vLLM 0.20.0,
   `--attention-backend FLASHINFER_MLA`, `--gpu-memory-utilization 0.95`,
   `--max-model-len 240000`). To be placed at
   `mlnode/packages/benchmarks/scripts/poc_validation/artifacts/minimaxai-minimax-m2.7.json`
   matching the existing format for Kimi and Qwen. Required for MiniMax
   PoC validation to function post-upgrade.

2. **Merge `gm/minimax` into `upgrade-v0.2.13`** once the artifact
   above is in place and testnet rehearsals stay green.

Set values now in `gm/minimax` (no longer outstanding):

- `Model.HfCommit = d494266a4affc0d2995ba1fa35c8481cbd84294b` (HF head
  as of 2026-04-20).
- `Model.VRam = 320`, `Model.ThroughputPerNonce = 5000`,
  `Model.UnitsOfComputePerToken = 10000`.
- `Model.ModelArgs = [--enable-auto-tool-choice, --kv-cache-dtype fp8,
  --tool-call-parser minimax_m2, --reasoning-parser minimax_m2_append_think]`
  (no explicit `--max-model-len`; vLLM uses the model default).
- `Model.ValidationThreshold = 0.922` (MiniMax), `0.940` (Qwen,
  newly populated), `0.900` (Kimi, lowered from 0.920).
- `PoCModelConfig.WeightScaleFactor = 0.3024` (MiniMax), `0.78` (Kimi,
  lowered from 1.26).
- `PoCModelConfig.PenaltyStartEpoch = 271` (MiniMax).
- `PoCStatTestParams (MiniMax) = {DistThreshold: 0.75, PMismatch: 0.10,
  PValueThreshold: 0.05}`.
