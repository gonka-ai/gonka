# Upgrade Proposal: v0.2.9

This document outlines the proposed changes for on-chain software upgrade v0.2.9. The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.9/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Once the PR is approved by a majority, a `v0.2.9` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

Creating the release from this branch (instead of `main`) minimizes the time that the `/deploy/join/` directory on the `main` branch contains container versions that do not match the on-chain binary versions, ensuring a smoother onboarding experience for new hosts.


## Testing

The on-chain upgrade from version `v0.2.8` to `v0.2.9` has been successfully deployed and verified on the testnet, including full PoC V2 activation and model consolidation.

Reviewers are encouraged to request access to the testnet environment to validate the upgrade or test the on-chain upgrade process on their own private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.9/inference-chain/app/upgrades/v0_2_9/upgrades.go).

Migration tasks:
- **Model consolidation**: Deletes all governance models except `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`.
- **Transfer Agent whitelist**: Configures allowed TA addresses for request gating.
- **Participant access params**: Sets registration and allowlist heights to 2475000.
- **PoC V2 activation**: Enables full PoC V2 with `WeightScaleFactor=0.262`, `InferenceValidationCutoff=2`, and `PocValidationDuration=480`.
- **Suspicious participant removal**: Removes 25 participants from allowlist who participated in epoch 155 POC but didn't vote for other participants.
- **POC slot reset**: Clears preserved slots to force full POC participation in the first V2 epoch.

## PoC V2 Full Activation

This upgrade enables **full PoC V2** — completing the transition from the tracking mode enabled in v0.2.8.

**After this upgrade:**
- PoC V2 is the main proof-of-compute engine with full weight and slashing enforcement.
- All nodes must participate in POC (no preserved slots from previous epoch).
- Guardian tiebreaker is enabled for undecided POC V2 votes.

**Key differences from v0.2.8:**
- v0.2.8 enabled V2 tracking only (`PocV2Enabled=false`, `ConfirmationPocV2Enabled=true`).
- v0.2.9 enables full V2 (`PocV2Enabled=true`, `ConfirmationPocV2Enabled=true`).

**First epoch behavior:**
- All nodes' POC_SLOT allocations are reset during upgrade.
- The epoch when V2 is enabled runs in grace mode (no punishment).
- Full V2 enforcement begins the following epoch.

## Model Consolidation

This upgrade consolidates the network to a single model: `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`.

**Chain side:**
- All other governance models are deleted during migration.

**API side:**
- Enforced model auto-switch ensures all nodes run qwen235B with `--tensor-parallel-size 4`.
- Nodes with different models are automatically redeployed with the correct model.

## Transfer Agent Whitelist

This upgrade introduces Transfer Agent (TA) access control to restrict which addresses can process inference requests.

**Chain side:**
- New `TransferAgentAccessParams` with `AllowedTransferAddresses` list.
- Validation in `StartInference` and `FinishInference` messages.

**API side:**
- Early enforcement in `/chat/completion` endpoint.
- Cache synced from chain on every new block for O(1) lookups.

**Behavior:**
- Empty whitelist = all TAs allowed (default).
- Non-empty whitelist = only listed TAs can process requests.

## Changes

### [PR #674](https://github.com/gonka-ai/gonka/pull/674) Missed inferences fix
*   Don't punish for missed inferences of non-preserved nodes during PoC.
*   Don't punish for missed inferences if participant doesn't support the model.
*   **Thanks to:** @x0152, @DimaOrekhovPS

### [PR #678](https://github.com/gonka-ai/gonka/pull/678) CPoC downtime penalty redistribution
*   Lower confirmation reward penalty is now transferred to the community pool instead of being lost.
*   Ensures penalties are redistributed fairly during CPoC downtime events.

### Guardian Tiebreaker for PoC V2 Voting
*   Adds guardians tiebreaker logic for undecided POC V2 votes.
*   When neither valid nor invalid votes reach majority, guardians can break the tie if they unanimously agree.
*   Trigger conditions: no majority exists, guardians enabled, at least one guardian voted, all voting guardians agree.

### Enforced Model Auto-Switch (API)
*   Automatically switches all nodes to `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` with `--tensor-parallel-size 4`.
*   Three-layer enforcement: config enforcement, state enforcement, and runtime verification.
*   Runtime verification queries vLLM `/v1/models` endpoint and triggers redeploy on mismatch.

### Transfer Agent Whitelist (Chain + API)
*   New chain parameter for allowed Transfer Agent addresses.
*   Early enforcement at API level before expensive operations.
*   Validation at chain level in `StartInference` and `FinishInference`.

### Suspicious Participant Removal
*   Removes 25 participants from the allowlist who exhibited suspicious behavior during epoch 155 POC.
*   These participants completed POC generation phase but did not vote for other participants at validation phase.
*   Addresses are permanently removed from the participant allowlist.

### Reset POC Slots for V2 Epoch
*   Resets `TimeslotAllocation[1]` (POC_SLOT) to `false` for all nodes.
*   Updates both `ActiveParticipants` and `EpochGroupData` structures.
*   Ensures all nodes participate in the first V2 POC phase.
