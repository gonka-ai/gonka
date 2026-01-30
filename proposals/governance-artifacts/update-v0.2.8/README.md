# Upgrade Proposal: v0.2.8

This document outlines the proposed changes for on-chain software upgrade v0.2.8. The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.8/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Once the PR is approved by a majority, a `v0.2.8` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

Creating the release from this branch (instead of `main`) minimizes the time that the `/deploy/join/` directory on the `main` branch contains container versions that do not match the on-chain binary versions, ensuring a smoother onboarding experience for new hosts.


## Testing

The on-chain upgrade from version `v0.2.7-post1` to `v0.2.8` has been successfully deployed and verified on the testnet, including the PoC V2 parameter migration.

Reviewers are encouraged to request access to the testnet environment to validate the upgrade or test the on-chain upgrade process on their own private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.8/inference-chain/app/upgrades/v0_2_8/upgrades.go).

Migration tasks:
- **Burn extra community coins**: Burns all coins from the `pre_programmed_sale` module account (`gonka1rmac644w5hjsyxfggz6e4empxf02vegkt3ppec`) which were inadvertently created during genesis.
- **Precompute BLS slot keys**: Generates and stores precomputed BLS slot public keys for the current epoch to enable the new optimized verification logic (see PR #609).
- **Set PoC V2 migration parameters**: Configures dual-mode migration with `ConfirmationPocV2Enabled=true` and `PocV2Enabled=false`, sets model ID to `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`, sequence length to 1024, and statistical test thresholds for V2 validation.

## PoC V2 Migration

For a smooth transition from PoC V1 to PoC V2, the chain must ensure that the majority of participants have switched to the new MLNode build supporting the `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` model before PoC V2 becomes the main PoC engine. This upgrade enables **tracking mode** to measure adoption without affecting weights.

**After this upgrade:**
- Regular PoC continues using V1 (on-chain batches, weight enforcement).
- First Confirmation PoC per epoch uses V2 for **tracking only** (no weight/slashing impact).
- V2 tracking results allow monitoring adoption before full activation.

**MLNode upgrade:**
- New versions: `ghcr.io/product-science/mlnode:3.0.12` (or `3.0.12-blackwell` for Blackwell GPUs).
- Backward compatible with 3.0.11 — can be upgraded before or after this on-chain upgrade.
- Must be upgraded before PoC V2 is fully enabled.

**Enabling full PoC V2:**
- PoC V2 will **not** activate automatically.
- Once adoption is sufficient, a **separate governance proposal** will set `poc_v2_enabled=true`.
- The epoch when V2 is enabled runs in grace mode (no punishment).
- Full V2 enforcement begins the following epoch.

## Changes

### [PR #505](https://github.com/gonka-ai/gonka/pull/505) Security Fixes for v0.2.7
Addresses multiple security vulnerabilities:
*   **SSRF & DoS:** Validates `InferenceUrl` to reject internal IPs and adds timeouts to prevent request hangs.
*   **Vote Flipping:** Prevents overwriting of PoC validations by rejecting duplicates.
*   **Batch Size Limits:** Enforces bounds on PoC batch sizes to prevent state bloat.
*   **PoC Exclusion:** Fixes `getInferenceServingNodeIds` to correctly exclude inference-serving nodes.
*   **Auth Bypass & Replay:** Binds `epochId` to signatures and validates authorization against the correct epoch.
*   **Thanks to:** @ouicate

### [PR #609](https://github.com/gonka-ai/gonka/pull/609) BLS optimized
*   Significantly optimizes BLS signature verification (from ~2s down to <10ms) by using the `blst` library and precomputing slot public keys.

### [PR #540](https://github.com/gonka-ai/gonka/pull/540) Remove ALL panic and Must from chain code
*   Removes `panic` and `Must` calls from chain code to prevent consensus failures.
*   Implements linting (`forbidigo`) and CI checks to enforce this rule.

### [PR #534](https://github.com/gonka-ai/gonka/pull/534) Security: prevent SSRF via executor redirect
*   Prevents SSRF attacks where a malicious executor redirects Transfer Agent requests to internal services (e.g., admin API).
*   Implements a custom HTTP client that disables following redirects.
*   **Thanks to:** @x0152

### [PR #544](https://github.com/gonka-ai/gonka/pull/544) Inference: defense-in-depth against int overflow
*   Fixes integer overflow vulnerabilities in escrow and cost calculations using checked arithmetic.
*   Adds hard caps for token counts and improves error handling to fail closed on overflows.
*   **Thanks to:** @ouicate

### [PR #506](https://github.com/gonka-ai/gonka/pull/506) Standardize floating point math
*   Replaces dangerous floating-point math with `shopspring/decimal` for deterministic calculations (e.g., Dynamic Pricing).
*   Updates reward exponent calculation to use a table-based approach for decay rates.

### [PR #536](https://github.com/gonka-ai/gonka/pull/536) Perf: optimize participants endpoint with single balance query
*   Optimizes the `/v1/participants` endpoint by replacing N gRPC calls with a single blockchain query.
*   Achieves ~500x speedup for large sets of participants.
*   **Thanks to:** @x0152

### [PR #553](https://github.com/gonka-ai/gonka/pull/553) Membership for correct epoch for Validation requests
*   Ensures validation rights are checked against the active participants of the target epoch, not the current one.
*   Fixes logic for sharing work coins and refunds during validation/invalidation.

### [PR #607](https://github.com/gonka-ai/gonka/pull/607) Fix(inference): update totalDistributed after debt deduction
*   Fixes a bug where `totalDistributed` was not updated after deducting debt, causing tokens to be lost instead of returned to governance.
*   **Thanks to:** @0xMayoor

### [PR #549](https://github.com/gonka-ai/gonka/pull/549) Disable future timestamp check for EA
*   Temporarily disables the future timestamp check in the External Adapter (EA) to prevent rejecting requests when the EA is behind the chain during high load.

### [PR #550](https://github.com/gonka-ai/gonka/pull/550) Negative coin balance for settle
*   Handles edge cases with negative coin balances by subtracting the negative amount from rewards instead of erroring.

### [PR #541](https://github.com/gonka-ai/gonka/pull/541) PoC validation, retry getting nodes
*   Adds retry logic for retrieving nodes during Proof of Compute (PoC) validation to improve robustness.

### [PR #551](https://github.com/gonka-ai/gonka/pull/551) Fix(bls): reject duplicate slot indices in partial signatures
*   Rejects partial signatures with duplicate slot indices to prevent verification failures during aggregation.
*   **Thanks to:** @0xMayoor

### [PR #563](https://github.com/gonka-ai/gonka/pull/563) Fix(inference): variable shadowing in direct payment path
*   Fixes a variable shadowing bug that caused errors (like `SendCoins` failures) to be swallowed during refunds.
*   **Thanks to:** @0xMayoor

### [PR #559](https://github.com/gonka-ai/gonka/pull/559) Burn extra pool coins, fix ValueDecimal validation
*   Burns coins from an inadvertently created account.
*   Fixes validation for `ValueDecimal` to correctly handle `nil` values.

### [PR #616](https://github.com/gonka-ai/gonka/pull/616) Integration test database, debugging assistance
*   Adds functionality to upload integration test results to BigQuery.
*   Includes fuzz testing and improved debugging logs.

### [PR #547](https://github.com/gonka-ai/gonka/pull/547) Updated script snippets and MacOS Tahoe 26.1 Docker settings
*   Updates documentation and adds Docker settings for running testermint locally on MacOS Tahoe.

### [PR #618](https://github.com/gonka-ai/gonka/pull/618) PoC v2 & Offchain PoC data
*   Integrates PoC directly into vLLM, enabling immediate switch from inference to PoC without offloading the model or loading a separate PoC model.
*   Migrates artifact storage off-chain using MMR (Merkle Mountain Range) commitments - only `root_hash` and `count` are recorded on-chain.
*   Adds statistical test-based validation with L2-distance mismatch rule and calibrated thresholds.
*   New chain messages: `SubmitPocValidationsV2`, `PoCV2StoreCommit`, `MLNodeWeightDistribution`.
*   Includes dual-mode migration strategy: V1 for regular PoC, V2 tracking for Confirmation PoC during rollout.
