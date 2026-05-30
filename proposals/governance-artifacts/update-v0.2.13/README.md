# Upgrade Proposal: v0.2.13

This proposal covers the v0.2.13 microrelease.

The release fixes confirmation PoC reward accounting, devshard escrow params,
complaint-response authz grants, upstream response parsing, participant
reactivation, node-manager gRPC defaults, and devshard storage growth.

It also adds a guardian-controlled emergency switch for disabling devshard
inference requests.

The upgrade also disables confirmation PoC for the rest of the upgrade epoch
so the new snapshot logic starts cleanly from the next epoch.

## Upgrade Plan

The node binary is upgraded through an on-chain software upgrade proposal.

The PR also updates `api` and `node` container versions in
`deploy/join/docker-compose.yml` for hosts joining after the on-chain upgrade.

Existing hosts are not required to manually update their `api` or `node`
containers as part of the chain upgrade.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.13/inference-chain/app/upgrades/v0_2_13/upgrades.go).

Migrations:

- Sets `DevshardEscrowParams.MaxEscrowsPerEpoch` to `500_000`.
- Sets `DevshardEscrowParams.MaxNonce` to `1_000_000`. The previous settlement
  path used a hardcoded `20_000` nonce limit.
- Adds addresses of several early miners and known brokers to
  `DevshardEscrowParams.AllowedCreatorAddresses`.
- Sets `GenesisOnlyParams.GenesisGuardianMultiplier` to `0.33334`, reducing
  genesis guardian power from about 34% to about 25% of adjusted voting power
  while early-network protection applies.
- Sets the chain-wide governance quorum to `0.25`. Quorum is computed against
  total chain voting power; with genesis guardians (25%) not voting, this gives
  an effective 1/3 quorum among the remaining 75% of voting power
  (`0.25 / 0.75 = 0.334`).
- Backfills `EpochGroupData.ConfirmationWeightScales` for the current epoch and
  clamps existing confirmation weights down to the new expected value.
- Backfills `MsgRespondDealerComplaints` authz grants on existing cold-to-warm
  ML ops pairs. v0.2.12 added this message to the permission list but did not
  migrate existing grants, so DAPIs that joined before v0.2.12 could not respond
  to dealer complaints.
- Disables confirmation PoC triggers for the rest of the upgrade epoch via a
  grace-epoch `UpgradeProtectionWindow` of 10000 blocks. The new snapshot logic
  starts from the next epoch.
- Adds MiniMax-M2.7 (`MiniMaxAI/MiniMax-M2.7`) as a governance model and PoC
  model config with `PenaltyStartEpoch = 278` (bootstrap activation epoch).
- Updates `PocParams.Models[*].WeightScaleFactor` to recalibrate against the
  Qwen-on-B200 reference after the vLLM 0.20.1 release. Kimi was too high on
  B* GPUs. Kimi = Qwen-on-B200 + 10% (top-tier premium), MiniMax = Qwen-on-B200:
  - Kimi (`moonshotai/Kimi-K2.6`): `0.78`
  - MiniMax (`MiniMaxAI/MiniMax-M2.7`): `0.3024`
- Updates `Model.ValidationThreshold` from cross-version vLLM results:
  - Qwen (`Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`): `0.940`
  - Kimi (`moonshotai/Kimi-K2.6`): `0.900`
  - MiniMax (`MiniMaxAI/MiniMax-M2.7`): `0.922`
- Adds `--enable-auto-tool-choice` to Kimi `ModelArgs` if missing.

## Changes

### inference-chain

- Confirmation PoC used different model sets for measured weight, preserved
  weight, and reward rescaling. During new-model bootstrap, this could reduce
  confirmation weight for honest miners serving both an eligible model and a
  not-yet-eligible model. v0.2.13 stores one epoch snapshot of confirmable
  models and weight-scale factors, then uses it for confirmation and reward
  calculations.
- `ConsecutiveInvalidInferences` was not reset when a participant became ACTIVE
  again. A host could return from invalid state and be invalidated again after
  one new failure. v0.2.13 resets the counter on reactivation and upcoming
  promotion.
- Devshard settlement now reads the nonce limit from
  `DevshardEscrowParams.MaxNonce` instead of a hardcoded constant.
- The upgrade adds addresses of several early miners and known brokers to the
  devshard creator allowlist without removing existing allowed creator addresses.
- Genesis guardians held about 34% of adjusted voting power, which made quorum
  hard to reach when they did not vote. The upgrade reduces guardian power to
  about 25% via `GenesisOnlyParams.GenesisGuardianMultiplier = 0.33334` and
  sets the chain-wide governance quorum to `0.25`. Quorum is computed against
  total bonded power; with guardians not voting, this gives an effective 1/3
  quorum among the remaining 75% of voting power (`0.25 / 0.75 = 0.334`).
- Adds `MsgSetDevshardRequestsEnabled`, a guardian-signed transaction for
  emergency disabling and re-enabling devshard inference requests.

### decentralized-api

- Some OpenAI-compatible upstreams return numeric `stop_reason` values.
  `Choice.StopReason` now accepts any JSON type, so those responses no longer
  fail unmarshalling.
- `NodeManagerGrpcPort` did not start by default when unset. It now defaults to
  `9400`, and join compose uses the same default so devshard can reach the API
  without manual config.
- The internal devshard service inside dapi uses the same devshard storage
  changes listed below, including pruning and Postgres support.

### devshard

- Devshard storage could grow forever because old escrow data stayed in one
  SQLite store. Storage is now epoch-scoped and prunes old epochs in the
  background, keeping the latest 3 epochs.
- Devshard can use Postgres as the primary store for larger deployments, with
  SQLite kept as a local fallback.
- Postgres data is partitioned by `epoch_id` for sessions, diffs, and
  signatures, so pruning can drop old epoch data cleanly.
