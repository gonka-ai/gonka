# Upgrade Proposal: v0.2.5

This document outlines the proposed changes for on-chain software upgrade v0.2.5. The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services and introduces the new service `bridge` for the native bridge with Ethereum. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

## Proposed Process
1. Active hosts review this proposal on GitHub.
2. Once the PR is approved by a majority, a `v0.2.5` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

Creating the release from this branch (instead of `main`) minimizes the time that the `/deploy/join/` directory on the `main` branch contains container versions that do not match the on-chain binary versions, ensuring a smoother onboarding experience for new hosts.

The `bridge` container can be started any time after upgrade by:

1. Pulling the latest changes from `main` branch (after `upgrade-v0.2.5` merged)
```
git pull
```

2. Start
```
source config.env && docker compose up bridge -d
```

It'll take some time to synchronize.

New MLNode container `v3.0.11` is fully compatible with `v3.0.10` and can be updated asynchronously at any time.
Additionally, the version `v3.0.11-blackwell` is introduced for Blackwell GPUs (CUDA 12.8+ required).

## Further Steps

The PR introduces 3 contracts:
- [liquidity pool](https://github.com/gonka-ai/gonka/tree/upgrade-v0.2.5/inference-chain/contracts/liquidity-pool/)
- [wrapped token](https://github.com/gonka-ai/gonka/tree/upgrade-v0.2.5/inference-chain/contracts/wrapped-token/)
- [Ethereum contract](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/proposals/ethereum-bridge-contact/BridgeContract.sol)

All contracts might be proposed for voter approval via separate proposals.


## Testing

### Testnet

The on-chain upgrade from version `v0.2.4` to `v0.2.5`  has been successfully deployed and verified on the testnet.

We encourage all reviewers to request access to our testnet environment to validate the upgrade. Alternatively, reviewers can test the on-chain upgrade process on their own private testnets.

## Migration 

The on-chain migration logic and default values for new parameters are defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/inference-chain/app/upgrades/v0_2_5/upgrades.go).

Specific data migrations are implemented in:
- [`migrations_confirmation_weight.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/inference-chain/x/inference/keeper/migrations_confirmation_weight.go): Initializes confirmation weights for the current epoch.
- [`migrations_bridge.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/inference-chain/x/inference/keeper/migrations_bridge.go): Removes legacy bridge state and artifacts.

**Note on Inactive Participant Exclusion:**
The parameters for the continuous exclusion of inactive participants (SPRT) are initialized with values that effectively disable the mechanism (requiring ~32k consecutive failures). This ensures the feature remains inactive until explicitly enabled via governance.

**Note on Confirmation PoC:**
The Confirmation PoC parameters are initialized to require 1 Confirmation PoC per Epoch.

## Changes
---
### Native Bridge 
Commit: [f7470c1eab3ebdda30dda90b0d81131b7b472a64](https://github.com/gonka-ai/gonka/pull/404/commits/168f7a8652260528c56acb25d918e7be5a19beca).

This commit introduces primitives for native bridge for the Ethereum blockchain and contracts for its integration. Details can be found [here](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/proposals/governance-artifacts/update-v0.2.5/bridge.md).

---

### BLS Signature fix
Commit: [f7470c1eab3ebdda30dda90b0d81131b7b472a64](https://github.com/gonka-ai/gonka/pull/404/commits/f7470c1eab3ebdda30dda90b0d81131b7b472a64).

This commit fixes a bug in BLS Group Public Key generation. 

---

### Participant Status Update

Commit: [101062297948f9a9574266adaf6439500502d6ba](https://github.com/gonka-ai/gonka/pull/404/commits/101062297948f9a9574266adaf6439500502d6ba)

This commit fixes the procedure for removing invalid and unavailable hosts from the EpochGroup.   
It also introduces a mechanism for continuously excluding inactive participants using SPRT.

Details: [here](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/proposals/invalid-participant-exclusion/README.md)

---

### Confirmation PoC

Commit: [e9dbf137b0fbb050c724877b4b607da88ab1dc64](https://github.com/gonka-ai/gonka/pull/404/commits/e9dbf137b0fbb050c724877b4b607da88ab1dc64)

This commit introduces Random Confirmation PoC - a new layer to verify inference-serving nodes maintain computational capacity during the whole epoch.

Details: [here](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/proposals/random-poc/README.md)

---

### New Schedule for `POC_SLOT=true` (nodes who serves inference during PoC)

Commit: [9ce1b6099529e69cdfd792f966efedb077c4ad86](https://github.com/gonka-ai/gonka/pull/404/commits/9ce1b6099529e69cdfd792f966efedb077c4ad86)

The chain automatically assigns a portion of MLNodes to serve inference during the next PoC phase to keep inference working. The initial version assigned 50% of weight per participant per model. 
To raise security, this commit proposes allocation of `POC_SLOT=true` by model weight percentages instead of per-participant halves, using a random subset of participants who served this model in the previous epoch.


Details: [here](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.5/proposals/poc-schedule-v2/README.md)

---


### Blackwell Support for MLNode and Fixes

Commit: [b77dcaca528ccfcf74e5f02d2bc90d55229a22f5](https://github.com/gonka-ai/gonka/pull/404/commits/b77dcaca528ccfcf74e5f02d2bc90d55229a22f5)

Fix for vLLM to support Blackwell GPUs (tested on B200).

---

### Account transfer fix

Commit: [4228a70579c195fcb7b989ddf19006d0ddf1e8ae](https://github.com/gonka-ai/gonka/pull/404/commits/4228a70579c195fcb7b989ddf19006d0ddf1e8ae)

The commit fixes the bug which used the full account balance to transfer instead of the spendable amount. Now locked coins and spendable are transferred separately.

---

### Paginator fix for `GetMembers`

Commit: [bbce9f4f296c4df9b722367f47b162c9cb6f6d46](https://github.com/gonka-ai/gonka/pull/404/commits/bbce9f4f296c4df9b722367f47b162c9cb6f6d46)

The commit fixes a bug with a missed paginator for the `GetMembers` function. This caused the selection of only a subset of miners. That might cause "unknown" status of validator.

---

### MLNode status check fixes, retry mechanism

Commit: [1352c131aff8713578033362b7dc2c3e22684277](https://github.com/gonka-ai/gonka/pull/404/commits/1352c131aff8713578033362b7dc2c3e22684277)

The commit fixes the MLNode status check to assign status "FAILED" if the node is not responding. Additionally, it adds a retry mechanism when a host has multiple MLNodes for the model.

---

### Recalculate total weight after punishment

Commit: [cf2d3931d2a1f8f9205194d12a4d7aa9b1d43980](https://github.com/gonka-ai/gonka/pull/404/commits/cf2d3931d2a1f8f9205194d12a4d7aa9b1d43980)

The commit fixes a bug where undistributed rewards paid to the first host included rewards for invalid participants.


---

### BLS Signature Fixes: Aggreagation and format 


Commit: [e4bbb293f79ed0f368900092c9e65393ca25bfdf](https://github.com/gonka-ai/gonka/pull/404/commits/e4bbb293f79ed0f368900092c9e65393ca25bfdf)

This commit fixes aggregation of partial signatures and align format with Etherium pre-compiled. 

### Changing default MLNode state to INFERENCE

Commit: [deefc869249c873377ae0feb0336aee3ac5034f1](https://github.com/gonka-ai/gonka/pull/404/commits/deefc869249c873377ae0feb0336aee3ac5034f1)

This commite changes default state for ML nodes from STOPPED to INFERENCE. This allows to validate missed inferences for claming a reward even if a node is disabled for the next epoch

Fixed a bug: ML node host and port updates are now correctly propagated, previously old addresses were cached and even after editing them via admin API PoC start requests would be sent to the old address

--