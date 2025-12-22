# Upgrade Proposal: v0.2.6

This document outlines the proposed changes for on-chain software upgrade v0.2.6. The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/gm/dev-0.2.6/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Once the PR is approved by a majority, a `v0.2.6` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

Creating the release from this branch (instead of `main`) minimizes the time that the `/deploy/join/` directory on the `main` branch contains container versions that do not match the on-chain binary versions, ensuring a smoother onboarding experience for new hosts.

## Optional: PostgreSQL for Payload Storage

Off-chain payloads use file-based storage by default, which is suitable for small nodes. For larger deployments, payloads can optionally be stored in PostgreSQL. The database is defined in a separate `docker-compose.postgres.yml` file.

It's recommended to deploy PostgreSQL on a separate machine or at least point its volume to a separate disk.

**Environment variables for PostgreSQL container** (`docker-compose.postgres.yml`):
- `POSTGRES_PASSWORD` (required)
- `POSTGRES_USER` (default: `payloads`)
- `POSTGRES_DB` (default: `payloads`)
- `POSTGRES_MIN_WAL_SIZE` (default: `4GB`)
- `POSTGRES_MAX_WAL_SIZE` (default: `16GB`)
- `POSTGRES_CHECKPOINT_TIMEOUT` (default: `15min`)

**Environment variables for API to connect to PostgreSQL** (`docker-compose.yml`):
- `POSTGRES_HOST` - PostgreSQL host address (if not set, file storage is used)
- `POSTGRES_PASSWORD` - PostgreSQL password
- `POSTGRES_PORT` (default: `5432`)
- `POSTGRES_USER` (default: `payloads`)
- `POSTGRES_DB` (default: `payloads`)

Start after upgrade:
```
git pull
source config.env && docker compose -f docker-compose.postgres.yml up -d
```

## Testing

The on-chain upgrade from version `v0.2.5` to `v0.2.6` has been successfully deployed and verified on the testnet.

We encourage all reviewers to request access to our testnet environment to validate the upgrade. Alternatively, reviewers can test the on-chain upgrade process on their own private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/gm/dev-0.2.6/inference-chain/app/upgrades/v0_2_6/upgrades.go).

Migration sets new parameters:
- PoC parameters (see "PoC Parameters On-Chain" in Changes section)
- `ValidationParams.ExpirationBlocks` = 150
- `ValidationParams.BinomTestP0` = 0.40 (temporary increase to ensure new payload storage stability)
- `BandwidthLimitsParams.MaxInferencesPerBlock` = 1000 (adds absolute inference count limit per block, in addition to existing bandwidth-based KB limiting; divided among participants)

## Changes

---

### Off-Chain Payloads

Commit: [477fb6e81](https://github.com/gonka-ai/gonka/commit/477fb6e81)

Moves inference prompts and response payloads off-chain. Only hashes are stored on-chain.

**Motivation:** Block size limit (22MB) and payload sizes (up to MBs for long responses) constrain throughput below compute capacity. Moving payloads off-chain reduces transaction size to ~500 bytes, removing bandwidth as a bottleneck.

Details: [proposals/offchain-payloads/README.md](https://github.com/gonka-ai/gonka/blob/gm/dev-0.2.6/proposals/offchain-payloads/README.md)

---

### Transaction Batching

Commit: [288b37732](https://github.com/gonka-ai/gonka/commit/288b37732)

Batching for StartInference/FinishInference transactions.

---

### PoC Parameters On-Chain

Commits: [86ebd4d65](https://github.com/gonka-ai/gonka/commit/86ebd4d65), [806b01616](https://github.com/gonka-ai/gonka/commit/806b01616), [f41e05142](https://github.com/gonka-ai/gonka/commit/f41e05142)

PoC parameters moved to on-chain governance. RTarget changed to 1.398077, increasing PoC difficulty ~2.5 times. Weights scaled by 2.5x to maintain absolute values.

---

### BLS DealerShared Recovery

Commit: [5b22aafd5](https://github.com/gonka-ai/gonka/commit/5b22aafd5)

Enables BLS secret recovery when container restarts.

---

### Nodes Always Available

Commit: [093c2e36a](https://github.com/gonka-ai/gonka/commit/093c2e36a)

Nodes remain available for inference even when disabled for next epoch.

---

### NATS Storage Fix

Commit: [ae938d357](https://github.com/gonka-ai/gonka/commit/ae938d357)

NATS messages retained for 24 hours instead of forever. Resolves issue with large `.dapi/.nats` directory.

---

### Force Recovery

Commit: [fe02ed509](https://github.com/gonka-ai/gonka/commit/fe02ed509)

Enables reward recovery even when stored seed is missing or corrupted. Seed can now be regenerated deterministically from epoch number, allowing recovery for any epoch.

---

### Epoch Performance Query

Commit: [08ad82a7b](https://github.com/gonka-ai/gonka/commit/08ad82a7b)

Adds `EpochPerformanceSummaryAll` query endpoint.

---

### GPU Distribution Fix

Commit: [fae7d20a5](https://github.com/gonka-ai/gonka/commit/fae7d20a5)

Limits PoC batch queue per GPU to prevent one GPU from accumulating all batches. Ensures even distribution across multiple GPUs.

---

### ClaimReward Performance

Commits: [19755e70d](https://github.com/gonka-ai/gonka/commit/19755e70d), [933eeb296](https://github.com/gonka-ai/gonka/commit/933eeb296), [8df8a1cf9](https://github.com/gonka-ai/gonka/commit/8df8a1cf9)

Optimizes ClaimReward transaction processing with reservoir sampling, debouncing, and ShouldValidate improvements. Reduces processing time from 1.6s to ~20ms for 1M inferences (~80x speedup). ShouldValidate call optimized from 2800ns to 1030ns (2.7x speedup).

---

### PoC Confirmation Weight Fix [Bounty Bug]

Commit: [b44d51e0c](https://github.com/gonka-ai/gonka/commit/b44d51e0c)

Fixes bug where PoC miners who didn't submit batches during Confirmation PoC incorrectly kept their confirmation weight. Now sets ConfirmationWeight to 0 for participants who didn't submit batches and have weight to confirm.

Found by: maksimenkoff  
Proposed Reward: 20,000 GNK

---

### Upgrade Rewards Distribution

Commit: [676f5e620](https://github.com/gonka-ai/gonka/commit/676f5e620)

Upgrade handler includes reward distribution for previous upgrades v0.2.4 and v0.2.5. Rewards are distributed to PR reviewers proportionally to node weight and contributions. Details in [upgrades.go](https://github.com/gonka-ai/gonka/blob/gm/dev-0.2.6/inference-chain/app/upgrades/v0_2_6/upgrades.go).
