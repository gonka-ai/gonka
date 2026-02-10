# Upgrade Proposal: v0.2.10

This document outlines the proposed changes for on-chain software upgrade v0.2.10. The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.10/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Once the PR is approved by a majority, a `v0.2.10` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

Creating the release from this branch (instead of `main`) minimizes the time that the `/deploy/join/` directory on the `main` branch contains container versions that do not match the on-chain binary versions, ensuring a smoother onboarding experience for new hosts.

## Testing

This branch includes upgrade-path changes and supporting testnet tooling. Reviewers are encouraged to request access to testnet environments to validate both node behavior and the on-chain upgrade process, or to replay the upgrade on private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.10/inference-chain/app/upgrades/v0_2_10/upgrades.go).

Migration tasks:
- **Validation slots default**: explicitly sets `PocParams.ValidationSlots=0` during migration. This keeps existing O(N^2) validation behavior after upgrade until sampling is enabled by governance parameter update.

## PoC Validation Sampling Optimization

This upgrade introduces a new PoC validation mechanism that reduces complexity from **O(N^2)** to **O(N x N_SLOTS)** by assigning each participant a fixed sampled set of validators.

Reference design and analysis: [`proposals/poc/optimize.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.10/proposals/poc/optimize.md)

Key points:
- Only assigned validators validate each participant when sampling is enabled.
- Sampling is deterministic on both chain and API sides (based on validation snapshot + `app_hash`).
- Decision threshold is strict supermajority of assigned slots (>66.7%).
- The feature is shipped in this release but **disabled by default** (`ValidationSlots=0`) and can be enabled via governance once rollout conditions are met.

## Changes

### [PR #710](https://github.com/gonka-ai/gonka/pull/710) PoC Validation Sampling Optimization
* Reduces validation complexity from quadratic to slot-based sampling.
* Adds deterministic slot assignment shared by chain and API, with snapshot-backed weight synchronization.
* Keeps backward-compatible fallback path when `ValidationSlots=0`.

### [PR #708](https://github.com/gonka-ai/gonka/pull/708) IBC Upgrade to v8.7.0
* Upgrades IBC stack to v8.7.0.
* Aligns chain interoperability components with current IBC release line.

### [PR #723](https://github.com/gonka-ai/gonka/pull/723) Testnet bridge setup scripts
* Adds bridge setup scripts for testnet operations.
* Improves reproducibility of bridge deployment and validation workflows.

### [PR #666](https://github.com/gonka-ai/gonka/pull/666) Artifact storage throughput optimization
* Improves PoC artifact storage throughput.

### [PR #688](https://github.com/gonka-ai/gonka/pull/688) Punishment statistics from on-chain data
* Uses on-chain data for punishment statistics with dynamic table selection.

### [PR #697](https://github.com/gonka-ai/gonka/pull/697) Portable BLST build for macOS test builds
* Uses a portable BLST build path for macOS test binaries.
* Improves reliability of local/test build pipeline on macOS hosts.

### [PR #712](https://github.com/gonka-ai/gonka/pull/712) Require proto-go generation matches committed code
* Enforces proto-go generation consistency in development flow.
* Prevents accidental drift between generated and committed protobuf code.

### [PR #711](https://github.com/gonka-ai/gonka/pull/711) PoC test params from chain state
* Replaces hardcoded PoC test defaults with chain state parameters.

### API hardening and reliability fixes
* [PR #634](https://github.com/gonka-ai/gonka/pull/634): add request body size limits to reduce DoS risk.
* [PR #638](https://github.com/gonka-ai/gonka/pull/638): fix unsafe type assertions in request processing.
* [PR #644](https://github.com/gonka-ai/gonka/pull/644): avoid rewriting static config on each startup.
* [PR #661](https://github.com/gonka-ai/gonka/pull/661): prevent API crash on short network drops.
* [PR #640](https://github.com/gonka-ai/gonka/pull/640): add unit tests for node version endpoint behavior.
* [PR #622](https://github.com/gonka-ai/gonka/pull/622): propagate refund errors in `InvalidateInference`.
* [PR #639](https://github.com/gonka-ai/gonka/pull/639): add missing return after error in task claiming path.
* [PR #643](https://github.com/gonka-ai/gonka/pull/643): sanitize nil participants in executor selection.
* [PR #545](https://github.com/gonka-ai/gonka/pull/545): minor bug fixes in API flow.

### Other fixes
* [PR #659](https://github.com/gonka-ai/gonka/pull/659): model assignment checks previous-epoch rewards.
* [PR #716](https://github.com/gonka-ai/gonka/pull/716): rename PoC weight function for clarity and correctness.
