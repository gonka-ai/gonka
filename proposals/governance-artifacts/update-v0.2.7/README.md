# Upgrade Proposal: v0.2.7

This document outlines the proposed changes for on-chain software upgrade v0.2.7. The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.7/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Once the PR is approved by a majority, a `v0.2.7` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

Creating the release from this branch (instead of `main`) minimizes the time that the `/deploy/join/` directory on the `main` branch contains container versions that do not match the on-chain binary versions, ensuring a smoother onboarding experience for new hosts.


Start after upgrade:
```
git pull
source config.env && docker compose -f docker-compose.postgres.yml up -d
```

## Testing

The on-chain upgrade from version `v0.2.6` to `v0.2.7` has been successfully deployed and verified on the testnet.

Reviewers are encouraged to request access to the testnet environment to validate the upgrade or test the on-chain upgrade process on their own private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.7/inference-chain/app/upgrades/v0_2_7/upgrades.go).

Migration sets new parameters:

- `GenesisGuardianParams.NetworkMaturityThreshold` = 15,000,000
- `GenesisGuardianParams.NetworkMaturityMinHeight` = 3,000,000
- Guardian addresses migrated from legacy `GenesisOnlyParams` into governance-controlled params (only if not already set)
- `DeveloperAccessParams.UntilBlockHeight` = 2,294,222 (inference gating for non-allowlisted developers)
- `DeveloperAccessParams.AllowedDeveloperAddresses` = predefined allowlist (governance-updatable)
- `ParticipantAccessParams.NewParticipantRegistrationStartHeight` = 2,222,222 (new host registration blocked until this height)
- `ParticipantAccessParams.BlockedParticipantAddresses` = placeholder blocklist (governance-updatable)
- `ParticipantAccessParams.UseParticipantAllowlist` = false (epoch allowlist disabled by default)

Migration also distributes rewards from the community pool:

- Epoch 117 rewards for nodes that didn't receive them (but successfully recovered) plus additional reward for all active nodes proportional to the chain halt duration
- Bounty program rewards for bug reports

## Changes

---

### Genesis Guardian Enhancement (Temporary)

Commits: [3c004c6dd](https://github.com/gonka-ai/gonka/commit/3c004c6dd), [0e5094ca0](https://github.com/gonka-ai/gonka/commit/0e5094ca0), [da1413498](https://github.com/gonka-ai/gonka/commit/da1413498)

Temporary reactivation of the Genesis Guardian Enhancement, a previously used defensive mechanism.

- Genesis Guardian parameters moved from genesis-only config to governance-controlled params
- Network maturity thresholds set: total power >= 15,000,000 AND block height >= 3,000,000
- Guardian addresses migrated from legacy params into governance-updatable `GenesisGuardianParams`
- Enhancement automatically deactivates when both maturity conditions are satisfied

---

### Developer Access Restriction

Commits: [3c004c6dd](https://github.com/gonka-ai/gonka/commit/3c004c6dd), [ca4b5f92f](https://github.com/gonka-ai/gonka/commit/ca4b5f92f), [fc3d13fb9](https://github.com/gonka-ai/gonka/commit/fc3d13fb9), [d5fae6671](https://github.com/gonka-ai/gonka/commit/d5fae6671)

Temporary restriction of inference execution to an allowlisted set of developer addresses.

- Inference requests (`MsgStartInference`, `MsgFinishInference`) gated by `requested_by` address
- Restriction active until block height 2,294,222
- Allowlist is governance-updatable via `DeveloperAccessParams`
- Non-allowlisted developers receive `ErrDeveloperNotAllowlisted`

---

### Participant Access Gating

Commits: [1d309fe27](https://github.com/gonka-ai/gonka/commit/1d309fe27), [d1523d1ca](https://github.com/gonka-ai/gonka/commit/d1523d1ca)

New participant registration pause and PoC blocklist enforcement.

- New host registration (`SubmitNewParticipant`, `SubmitNewUnfundedParticipant`) blocked until height 2,222,222
- PoC blocklist enforced in `MsgSubmitPocBatch` and `MsgSubmitPocValidation`
- Adds `MsgAddParticipantsToAllowList`, `MsgRemoveParticipantsFromAllowList`, and `QueryParticipantAllowList` for future governance-controlled epoch allowlist (disabled by default)

---

### PoC Transaction Filtering

Commits: [1644047b9](https://github.com/gonka-ai/gonka/commit/1644047b9), [2dbdcca00](https://github.com/gonka-ai/gonka/commit/2dbdcca00)

Protocol-level filtering of stale PoC transactions and improved tx-manager reliability.

- Ante handler rejects too-late `MsgSubmitPocBatch` and `MsgSubmitPocValidation` during CheckTx
- API tx-manager adds block-based deadlines per message type (PoC: 240 blocks, inference: 150 blocks)
- Business logic errors (e.g., duplicate validation, participant not found) fail immediately instead of retrying
- Batching for `MsgSubmitPocBatch` and `MsgSubmitPocValidation` transactions

---

### Inference Completion Handling

Commit: [2c05788d5](https://github.com/gonka-ai/gonka/commit/2c05788d5)

Fixes incorrect accounting of failed inference requests.

- Malformed or broken payloads no longer cause inferences to be marked as missed
- Improves resilience around failed inference handling in the API

---

### Governance-Owned Leftovers

Commit: [cf483b34e](https://github.com/gonka-ai/gonka/commit/cf483b34e)

Settlement and bitcoin reward remainder accounting.

- Expired/unclaimed `SettleAmount` transferred to governance module account instead of burned
- Bitcoin rewards: missed-share and rounding remainder transferred to governance and tracked via `BitcoinResult.GovernanceAmount`

---

### Epoch 117 + Bounty Rewards Distribution

Commits: [3d8d4caf2](https://github.com/gonka-ai/gonka/commit/3d8d4caf2), [a4828a1d0](https://github.com/gonka-ai/gonka/commit/a4828a1d0)

Reward distribution executed during upgrade.

- Nodes active during Epoch 117 that didn't receive their epoch reward get the recovered amount
- All nodes active during Epoch 117 receive an additional payout proportional to the chain halt duration
- Bounty program rewards distributed for reported bugs
