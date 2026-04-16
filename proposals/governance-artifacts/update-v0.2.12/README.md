# Upgrade Proposal: v0.2.12

This document outlines the proposed changes for on-chain software upgrade v0.2.12.   
The `Changes` section details the major modifications, and the `Upgrade Plan` section describes the process for applying these changes.

## Upgrade Plan

This PR updates the code for the `api` and `node` services. The PR modifies the container versions in `deploy/join/docker-compose.yml` and introduces a new `versiond` service in the join stack.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.12/docs/upgrades.md).

Existing hosts are **not** required to upgrade their `api` and `node` containers as part of the on-chain upgrade itself. After the upgrade, hosts must deploy the new `versiond` service and update and redeploy `proxy` with `VERSIOND_SERVICE_NAME=versiond` and `GONKA_API_EXEMPT_ROUTES=chat inference poc/proofs devshard` so `/devshard/<version>/*` traffic is routed through `proxy -> versiond -> devshardd`. New hosts joining after the upgrade should use the updated container versions from this compose file.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

## Testing

The on-chain upgrade from version `v0.2.11` to `v0.2.12` has been successfully deployed and verified on the testnet. No regression in core functionality or performance has been observed during testing. More testing will be executed leading up to the upgrade.

Reviewers are encouraged to request access to testnet environments to validate both node behavior and the on-chain upgrade process, or to replay the upgrade on private testnets.

## Migration

The on-chain migration logic is defined in [`upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.12/inference-chain/app/upgrades/v0_2_12/upgrades.go).

Migrations:

- Auto-creates `x/feegrant` allowances for every existing cold-to-warm ML ops authz grant to support transaction fees.
- Initializes `FeeParams`.
- Migrates singular PoC model parameters into the new multi-model `PocParams.Models` list and initializes `DelegationParams`.
- Clears legacy PoC v2 data (which used old key layouts) and seeds new pruning state markers for the new multi-model collections.
- Backfills `ActiveParticipant.VotingPowers` and `EpochGroupData` subgroup voting power for the current epoch to ensure seamless PoC validation post-upgrade.
- Removes unused `TopMiners` and training states (training will be moved to an off-chain architecture similar to devshards).

- [ ] Add koeff and params for new model

## Changes

### [Multi-model PoC](https://github.com/gonka-ai/gonka/pull/1039)

Historically, PoC has been tied to a single base model. While the network aims to support multi-model inference, relying on a single-model PoC is not secure enough.

If the network served several models but only checked one during PoC, an attacker could spin up hardware just for the check and shut it down afterward. To prevent this, PoC must start immediately on the exact model being validated, proving the hardware is present and running that model *right now* with no window to swap deployments.

To support multiple models, this upgrade runs PoC for each model independently in separate model groups. The core mechanics:

- Each governance-approved model gets its own PoC group. PoC runs for all eligible groups in parallel.
- Weight is split into two layers. `PoC weight` is model-local and drives inference routing and inference rewards inside that specific group. `Consensus weight` is the total weight aggregated across all eligible model groups (using model-specific coefficients) that determines block signing power, voting power, and bitcoin-style rewards.
- Because not every Host can run every model, a Host not serving a model can delegate its consensus weight to a group member for PoC validation only (this does not affect block signing or governance voting power). This preserves the existing security model: a model group must reach a 2/3 validation threshold of the *total network consensus weight*, not just the group-local weight, even if its direct members hold less than that total amount.
- For each active model, Hosts must explicitly choose their participation mode (DIRECT, DELEGATE, REFUSE). Hosts who do nothing receive a penalty. Penalties are skipped during a model's initial grace period.

The current base model remains the starting group for bootstrapping additional models. The exact model coefficients and final parameter values are not yet part of this PR.

### Transaction fees for spam prevention ([#937](https://github.com/gonka-ai/gonka/pull/937), [#981](https://github.com/gonka-ai/gonka/pull/981))

v0.2.12 turns on consensus-level transaction fees for the first time. Before this upgrade, any funded account could broadcast an unlimited number of transactions at zero cost, because the chain relied only on per-validator `minimum-gas-prices` configuration, which is mempool-only and trivially bypassed by a malicious block proposer. This left governance proposals, bank sends, staking operations, collateral management, reward claims, bridge operations, and CosmWasm calls without any economic friction against abuse.

v0.2.12 introduces a governance-controlled `FeeParams.min_gas_price_ngonka` enforced during both `CheckTx` and `DeliverTx`. The initial value is **10 ngonka per gas unit**, which works out to ~800,000 ngonka (0.0008 GNK) for a typical 80k-gas transaction. Negligible for legitimate users, but meaningful at spam volumes: flooding 100,000 transactions would cost an attacker around 80 GNK. Governance can adjust the parameter without a chain upgrade.

Protocol-obligation messages — PoC submissions, validation messages, inference start/finish, BLS DKG rounds — are made fee-exempt via a `NetworkDutyFeeBypassDecorator`. These are already rate-limited by timing windows, duplicate checks, and epoch-scoping, so adding fees would not improve their spam resistance while complicating automated node operation.

For Host sybil resistance specifically, `MsgPoCV2StoreCommit` charges a two-component fee: a base validation cost charged once per participant per epoch (covering the GPU work validators have to perform), plus a count-proportional cost charged on each count delta. A sybil claiming high compute weight across many fake participants pays proportionally to the weight they are trying to claim, making large-scale sybil attacks economically prohibitive.

### Devshards (formerly "subnets") — standalone, versioned runtime ([#1045](https://github.com/gonka-ai/gonka/pull/1045))

Previously, the devshard runtime lived inside the main DAPI process. Upgrading devshards meant rebuilding, redeploying, and restarting the entire DAPI, which slowed down development and added risk to all Host work (including inference, PoC, and Confirmation PoC).

To solve this, v0.2.12 decouples devshards into a standalone, versioned runtime managed by a new service called `versiond`.

- `versiond` automatically downloads and runs devshard binaries approved by on-chain governance.
- Multiple devshard versions can run side-by-side. Traffic to `/devshard/<version>/*` is routed to the corresponding binary, while the legacy `/v1/devshard/*` route remains active during the transition.
- The standalone devshard directly communicates with MLNodes during inference but does not manage their lifecycle, cleanly separating the roles of MLNode manager (DAPI) and client.
- Each session is cryptographically bound to the specific binary version that served it. The settlement payload now includes a cleartext `version` field, ensuring a session cannot mix responses from different versions.
- The term "subnet" is entirely replaced by "devshard" across the codebase. Additionally, float math in devshard settlement has been replaced with deterministic integer arithmetic to eliminate consensus-failure risks.

### Certik audit fixes

The Certik audit produced a number of findings across the chain, bridge, BLS, and inference modules. This upgrade addresses the full set of findings flagged for v0.2.12: GEB-44, GEB-45, GEB-46 (ETH/WGNK address collision), GEB-51, GEB-54, GOC-15, plus a second batch of bridge and BLS findings. No known findings from the audit remain unaddressed.

### Removals and cleanups

- The unused TopMiner reward logic is removed, and the upgrade handler clears the `TopMiners` collection during migration with no financial impact.
- The in-chain training placeholder is removed. The feature was never used and carried security risks, so training is moving to an off-chain architecture similar to devshards.
- Developers no longer need to register as a Participant to run inference, as an account with a public key on chain is now sufficient.

### Protocol hardening and correctness

- PoC v2 RNG is stronger because the new mechanism addresses the 32-bit entropy flaw that made forged proofs feasible. It will be activated via an additional governance vote once MLNodes are updated.
- The MLNode version is now propagated to chain state so the network always reflects the exact software version each node is running. This allows the network to track adoption of new MLNode versions.
- A long-standing consensus issue in the BLS DKG dealing phase is corrected.
- Validator slashing now consistently aligns with the required-collateral model instead of legacy behavior.
- Fixes a bug where `inference_finished` event parsing failed on zero-timestamps.

### API and tooling improvements

- The DAPI now accepts multipart-encoded OpenAI requests alongside JSON to improve compatibility with upstream SDKs.
- More accurate HTTP status codes and error shapes, including 400/422 for malformed payloads, ensure upstream OpenAI SDKs behave correctly.
- The DAPI exposes node acquisition RPCs in the private network with TTL eviction via a new NodeManager gRPC server, which lets external services like devshards coordinate MLNode usage cleanly.
- End-to-end inference validation tests in Testermint are updated to cover status transitions.
- Deployment documentation is updated with multisig and access-control setups for production operators.

#### Action will be required

##### Multi-model

For Hosts, participation logic is now evaluated on a model-by-model basis. For each governance-approved model, a Host must choose one of the following participation modes:

- DIRECT mode means the Host runs the model and participates in its PoC directly.
- DELEGATE mode means the Host delegates its PoC validation power for that model to another Host running it.
- REFUSE mode means the Host explicitly refuses participation for that model.
- INTENT mode means the Host declares early intent to participate before deploying hardware for models approved but not yet active.
- NONE mode means the Host does nothing for that model (this will result in a penalty).

*Note: Delegation is necessary because not every Host can realistically run every model due to hardware constraints. Without delegation, a model group whose direct members hold less than 2/3 of the total network weight could never pass PoC validation.*

- [ ] Instruction with exact commands to be published

##### Fees — but only minimally

The v0.2.12 upgrade introduces transaction fees, which means the warm key (ML operational key) needs to pay for automated transactions like reward claims and hardware diff updates. Because the warm key holds zero balance by design, it needs an `x/feegrant` allowance from the cold account.

For existing Hosts, this migration is fully automatic. The upgrade handler will automatically create an `x/feegrant` allowance (valid for 1 year) from the cold key to the warm key.

**Host's responsibility:** Ensure the cold account holds at least **100 GNK**. This balance will be enough to cover the expected automated fee burden for several years, plus any manual operations.

New Hosts onboarding after the upgrade will run the same `grant-ml-ops-permissions` command as before; the command has been updated to issue both the authz grants and the `x/feegrant` allowance in a single transaction.

The full onboarding and upgrade flows are documented in [`docs/host_onboarding.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.12/docs/host_onboarding.md).

- [ ] Instruction with exact commands to be published
