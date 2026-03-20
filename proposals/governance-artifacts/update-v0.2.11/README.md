# Upgrade Proposal: v0.2.11

This document outlines the proposed changes for on-chain software upgrade v0.2.11.   
The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.11/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

It also updates CosmWasm contract artifacts for the community sale and liquidity pool, adds bridge/testnet operational scripts for IBC trading support, and introduces a new `subnet/` package used by the new inference architecture.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

## Testing

The on-chain upgrade from version `v0.2.10` to `v0.2.11` has been successfully deployed and verified on the testnet. No regression in core functionality or performance has been observed during testing. More testing will be executed leading up to the upgrade.

Reviewers are encouraged to request access to testnet environments to validate both node behavior and the on-chain upgrade process, or to replay the upgrade on private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.11/inference-chain/app/upgrades/v0_2_11/upgrades.go).

Migrations:
- Sets `ValidationParams.ClaimValidationEnabled = false`.
- Rebuilds active participant caches for the current and previous epoch.
- Migrates epoch-group validations into the new entry-based format.
- Community-sale CosmWasm contract migration.

## Changes

### [PR #877](https://github.com/gonka-ai/gonka/pull/877) Inference shards (Experimental)
- Introduces subnet-based inference flow, moving per-inference coordination off-chain.
- The chain now handles only session setup, escrow, and settlement.
- Adds support for subnet state, transport, signing, storage, settlement, and API integration.
- **Note:** This feature is currently experimental and under limited access. For reference design and architecture, see [`proposals/inference/`](https://github.com/gonka-ai/gonka/tree/main/proposals/inference).


### [PR #812](https://github.com/gonka-ai/gonka/pull/812) StartInference and FinishInference performance improvements
- Reduces unnecessary state writes and query overhead for `MsgStartInference` and `MsgFinishInference`.
- Simplifies stats handling and cuts work done during the inference lifecycle for better block execution stability.


### [PR #760](https://github.com/gonka-ai/gonka/pull/760) Unified Permissions
- Consolidates message-permission checks across the inference module.
- Removes duplicated authorization logic to make permission behavior more explicit and testable.


### [PR #779](https://github.com/gonka-ai/gonka/pull/779) Inference msgs optimization: optimize key verification
- Reduces cryptographic verification overhead in the inference message path.
- Avoids repeating signature checks where protocol guarantees make them redundant.

### [PR #874](https://github.com/gonka-ai/gonka/pull/874) MsgValidation and MsgClaimRewards performance optimization
- Reduces hot-path lookups and adds transient caching in validation and reward-claiming paths.
- Restructures validation/reward logic and introduces state pruning support.

### [PR #822](https://github.com/gonka-ai/gonka/pull/822) BLS related fixes based on Certik audit
- Applies Certik audit fixes to the BLS module.
- Fixes threshold-validation and duplicate-slot handling issues for distributed key generation and threshold-signing flows.

### [PR #814](https://github.com/gonka-ai/gonka/pull/814) IBC Trade Support
- Introduces governance-controlled support for trading approved IBC-denominated assets.
- Includes chain message/query changes and contract updates for the community sale and liquidity pool.

### [PR #868](https://github.com/gonka-ai/gonka/pull/868) Required-collateral aware slashing flow
- Bases slashing penalties on required collateral rather than the full deposited amount.
- Makes the slashing model more proportional for participants who over-deposit relative to the minimum.

### [PR #888](https://github.com/gonka-ai/gonka/pull/888) Fix: collateral
- Fixes reward calculation for undercollateralized miners, ensuring actual collateral accurately reduces effective earning power.

### [PR #775](https://github.com/gonka-ai/gonka/pull/775) fix: redirect slashed coins to gov
- Redirects slashed collateral to governance-controlled destinations.

### Other changes
- [PR #867](https://github.com/gonka-ai/gonka/pull/867) Fix the application.db bloat issue.
- [PR #835](https://github.com/gonka-ai/gonka/pull/835) Add Batch Transfer With Vesting.
- [PR #773](https://github.com/gonka-ai/gonka/pull/773) feat: delete governance model.
- [PR #675](https://github.com/gonka-ai/gonka/pull/675) security: update CometBFT to v0.38.21 (CSA-2026-001).
- [PR #543](https://github.com/gonka-ai/gonka/pull/543) fix: data race conditions.
- [PR #815](https://github.com/gonka-ai/gonka/pull/815) Update CONTRIBUTING.md.
- [PR #807](https://github.com/gonka-ai/gonka/pull/807) Update issue templates.

## Proposed Bounties

Bounty ID | Sum GNK | Bounty Explanation | GitHub ID
-- | -- | -- | --
PR #543 | 2500 | extra bounty for a comprehensive review of all cases where the data race conditions  fix was needed | @x0152
Issue #628 | 25000 | PoC integration into vllm v0.11.1 [report](https://github.com/axeltec-software/gonka/tree/axeltec/poc-integration/proposals/poc-integration-vllm-v0.11)  |  [Axel-t](https://www.axel-t.com/), @Red-Caesar
-- | 10000 | report of series of prompts resulting in vllm HTTP 502 response, significant impact, was already used for intentoinal greifing | @blizko
-- | 1000 | report of dust transaction vulnerability extending blocks | @blizko
-- | 5000 | report of Remote DoS of Validator PoC Software via dist Assertion | @ouicate
-- | 5000 | report of State Bloat PoC and End-Block DoS via Unbounded Batch / Validation Payloads | @ouicate
-- | 750 | report of Bridge Ethereum Address Parsing Silently Falls Back to Zero Bytes (Loss/Misdirection of Funds) | @ouicate
PR #775 | 1000 | planned task | @x0152
PR #773 | 1250 | planned task | @x0152
[qdanik/vllm/pull/5](https://github.com/qdanik/vllm/pull/5) | 12000 | vLLM 0.15.1 Compatibility Experiments - basis for next ML node version | @qdanik
[qdanik/vllm/pull/6](https://github.com/qdanik/vllm/pull/6) | 15000 | vLLM 0.15.1 Compatibility Experiments - basis for next ML node version. covering simultanious PoC and inference | @qdanik
-- | 5000 | report of wind down window vulnerability fixed in PR #767 | @qdanik
Issue #797 | 1000 | collective solving of nodes unable to join from snapshots - proposed valuable hypothesis | @akup
Issue #797 | 3000 | collective solving of nodes unable to join from snapshots - found source problem | @x0152
Issue #780 | 750 | collective solving StartInference and FinishInference issue | @hleb-albau
Issue #781 | 5000 | collective solving StartInference and FinishInference issue | @x0152
Issue #782 | 5000 | collective solving StartInference and FinishInference issue | @akup
PR #867 | 7500 | important issue that affected many participants, not a vulnerability, fairly easy fir; adding extra payment for fully testing and providing results of the test together with the fix | @Lelouch33
Issue #730 | 22500 | vLLM 0.15.1 Compatibility Experiments - basis for next ML node version | @clanster, @baychak
PR #835 | 5000 | Batch Transfer With Vesting implementation, huge kudos for figuring out how to use testnet | @huxuxuya
PR #868 | 5000 | collateral slashing vulnerability and fix; low severity: low risk, medium likelyhood, organic | @qdanik
v0.2.11 | 7500 | release management | @akup
v0.2.11 | 7500 | release management | @x0152
v0.2.10 | 2500 | upgrade review | @yapion
v0.2.10 | 2500 | upgrade review | @blizko
v0.2.10 | 2500 | upgrade review | @x0152

