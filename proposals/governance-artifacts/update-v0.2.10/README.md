# Upgrade Proposal: v0.2.10

This document outlines the proposed changes for on-chain software upgrade v0.2.10. The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml`.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.10/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers. The updated container versions are intended for new hosts who join after the on-chain upgrade is complete.

To apply the new vLLM model parameters, mlnode must be restarted after the on-chain upgrade. The safest approach is:

```
docker restart join-mlnode-1
```

The upgrade of MLNode to more reliable versions `ghcr.io/product-science/mlnode:3.0.12-post4` / `ghcr.io/product-science/mlnode:3.0.12-post4-blackwell` is recommended.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Once the PR is reviewed by the community, a `v0.2.10` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

Creating the release from this branch (instead of `main`) minimizes the time that the `/deploy/join/` directory on the `main` branch contains container versions that do not match the on-chain binary versions, ensuring a smoother onboarding experience for new hosts.

## Testing

The on-chain upgrade from version `v0.2.9` to `v0.2.10` has been successfully deployed and verified on the testnet. PoC time-based weight normalization has been validated in the testnet environment. No regression in core functionality or performance has been observed during testing.

Reviewers are encouraged to request access to testnet environments to validate both node behavior and the on-chain upgrade process, or to replay the upgrade on private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.10/inference-chain/app/upgrades/v0_2_10/upgrades.go).

Migrations:
- **Validation slots default**: explicitly sets `PocParams.ValidationSlots=0` during migration. This keeps existing O(N^2) validation behavior after upgrade until sampling is enabled by governance parameter update.
- **PoC normalization default**: explicitly sets `PocParams.PocNormalizationEnabled=true` during migration to enable time-based weight normalization.
- **Model parameter update**: Updates `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` with tool calling args (`--enable-auto-tool-choice`, `--tool-call-parser hermes`) and validation threshold `0.958`.

## PoC Validation Sampling Optimization

This upgrade introduces a new PoC validation mechanism that reduces complexity from **O(N^2)** to **O(N x N_SLOTS)** by assigning each participant a fixed sampled set of validators.

Reference design and analysis: [`proposals/poc/optimize.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.10/proposals/poc/optimize.md)

Key points:
- Only assigned validators validate each participant when sampling is enabled.
- Sampling is deterministic on both chain and API sides (based on validation snapshot + `app_hash`).
- Decision threshold is strict supermajority of assigned slots (>66.7%).
- The feature is shipped in this release but **disabled by default** (`ValidationSlots=0`) and can be enabled via a governance proposal that changes the `ValidationSlots` parameter to a non-zero value once rollout conditions are met.

## PoC Weight Normalization by Real PoC Time

This upgrade normalizes PoC participant weights by actual PoC elapsed time to reduce block-time drift effects and keep weight outcomes consistent with real execution duration.

Key points:
- Adds `PocParams.PocNormalizationEnabled` parameter for time-based normalization control.
- Captures generation start and exchange end timestamps in `PoCValidationSnapshot`.
- Applies a normalization factor derived from expected stage duration vs actual elapsed time.
- Applies to both regular PoC and confirmation PoC weight calculations.
- Enabled by default in this upgrade (`PocNormalizationEnabled=true`).

## Upgrade Grace Period

To ensure a smooth upgrade transition:
- Confirmation PoC will not be triggered for the first 3000 blocks (~5 hours) after upgrade.
- Miss/invalid punishment rates are relaxed for the entire grace epoch (binom_test_p0 set to 0.5).
- Regular PoC operates normally during the grace period.

## Changes

### [PR #710](https://github.com/gonka-ai/gonka/pull/710) PoC Validation Sampling Optimization
* Reduces validation complexity from quadratic to slot-based sampling.
* Adds deterministic slot assignment shared by chain and API, with snapshot-backed weight synchronization.
* Keeps backward-compatible fallback path when `ValidationSlots=0` and includes upgrade-time default of `ValidationSlots=0` for safe rollout.

### [PR #725](https://github.com/gonka-ai/gonka/pull/725) PoC weight normalization on real PoC time
* Adds time-based PoC weight normalization to reduce sensitivity to block-time variance.
* Introduces `PocNormalizationEnabled` in PoC params and uses validation snapshot timestamps to compute normalization factor.
* Integrates normalization into both regular PoC and confirmation PoC weight calculations.
* Upgrade handler enables normalization by default for `v0.2.10`.

### [PR #767](https://github.com/gonka-ai/gonka/pull/767) Upgrade grace period, tool calling, and PoC timing fix
* Adds grace epoch protection for the upgrade epoch: extended CPoC window (3000 blocks) and relaxed miss/invalid thresholds.
* Updates Qwen model with tool calling support (`--enable-auto-tool-choice`, `--tool-call-parser hermes`).
* Adjusts validation threshold from 0.970917 to 0.958.
* Deprecates `poc_exchange_duration` parameter (set to 0 in upgrade). API artifact acceptance now aligns with chain exchange windows using explicit block height checks instead of relying on phase alone. Fixes a gap where chain accepted nonces longer than API.

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

### [PR #641](https://github.com/gonka-ai/gonka/pull/641) Streamvesting transfer with vesting
* Adds `MsgTransferWithVesting` RPC and message type in the `streamvesting` module. Enables sender-to-recipient token transfers with vesting over N epochs (default: 180 epochs when not specified).
* Adds safety limits to prevent abusive requests: max `3650` vesting epochs and max `10` coin denoms per transfer.

### API hardening and reliability fixes
* [PR #634](https://github.com/gonka-ai/gonka/pull/634): add request body size limits to reduce DoS risk.
* [PR #727](https://github.com/gonka-ai/gonka/pull/727): follow-up for #634, pass response writer to `http.MaxBytesReader` and align tests.
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

## Proposed Bounties

| PR/Issue | Sum GNK | Bounty Explanation |
|-----------|---------|---------------------|
| PR #661 | 500 | Valid fix for minor vulnerability that was previously reported in issue #422 |
| PR #644 | 700 | Planned task, not a vulnerability, important for the network. |
| PR #659 | 10,000 | Detailed report and fix for a Medium risk vulnerability. |
| Report | 5,000 | First report of the vulnerability fixed in #659 |
| PR #545 | 1,000 | Report and fix of low risk vulnerability. Extra appreciation for discovering and reporting it during the review of another PR. |
| PR #640 | 100 | Valid minor bug fix. |
| Issue #422 | 500 | First report and suggested fix. Fixed in PR #661 |
| PR #638 | 100 | Valid minor bug fix. |
| PR #634 | 100 | Valid minor bug fix. |
| Report | 5,000 | Independent report on the issue addressed by PR #710. |
| PR #643 | 500 | Report and fix of low risk vulnerability. |
| PR #641 | 1,500 | Valid implementation of a planned task. |
| PR #622	| 700 |	Valid minor vulnerability report and fix.	|
| PR #688 | 1,500 | Valid implementation of a planned task with adjusting scope, important for the network. |

* Review of previous upgrades with meaningful feedback - 2,500 GNK each :
  * v.0.2.9: [blizko](https://github.com/blizko) & [x0152](https://github.com/x0152)
  * v.0.2.8: [blizko](https://github.com/blizko), [x0152](https://github.com/x0152), [ouicate](https://github.com/ouicate), [jacky6block](https://github.com/jacky6block) & [akup](https://github.com/akup)
