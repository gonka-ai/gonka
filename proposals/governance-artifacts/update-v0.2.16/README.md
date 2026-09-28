# Upgrade Proposal: v0.2.16

The v0.2.16 upgrade includes protocol changes and bug fixes across the chain and API node.

## Trusted Weight

Before v0.2.16, a sudden increase in claimed compute immediately increased a participant's power in governance, BLS, and PoC validation. This gave unconfirmed capacity influence over consensus and over validation of other participants.

The upgrade limits that power to compute confirmed in the previous epoch. New or returning participants start at 0, and a failed confirmation sets the next baseline to 0. New compute still earns rewards immediately. Only trust-sensitive power waits one confirmed epoch.

## Dynamic Coefficients v1

The network needs predictable throughput for each model so users can rely on its availability. The target throughput for each model should follow expected demand.

With fixed coefficients, an entire hardware class tends to switch to the same model. This makes capacity per model difficult to balance and predict.

This upgrade lets governance set a target percentage of network compute and a coefficient range for each model. The protocol dynamically adjusts the coefficient inside that range to move compute toward the target. Compute above the target is scored at the minimum coefficient, which discourages oversupply.

Governance initially sets targets from demand estimates and data sources such as OpenRouter. Later versions can aggregate host estimates and eventually use on-chain model usage. This version changes incentives only. It does not automatically switch the models deployed by a host. At upgrade, the minimum and maximum both equal the current scale, so coefficients and rewards stay unchanged until governance opens the ranges.

## Fee

Historically, the protocol has charged no transaction fees. An attacker can therefore submit high-volume messages at little cost while every validator pays the processing and storage cost.

The upgrade groups transaction types and adds per-message gas rules so each group can be priced separately. All groups are disabled by default and charge nothing. Governance can enable and price them later. The upgrade proposal info can also enable groups at upgrade height.

## PoC Challenge

PoC and random Confirmation PoC prove a host's claimed capacity. During the rest of the epoch, inference statistics check that this hardware is used for work assigned by the protocol. A high rate of missed or invalid inferences can remove a host. This provides a strong ongoing check, but its sensitivity depends on inference volume and may not reveal every gap between claimed and available capacity. PoC Challenge adds an additional security layer for these cases.

An approved challenger can require one active host to leave inference and run PoC at full capacity until the next regular PoC. To open the challenge, the challenger locks a payment equal to a fraction of the target's remaining epoch reward. If the target passes, it receives the payment. If it fails, the challenger is refunded and the target receives the same penalty as for a failed Confirmation PoC. Inference missed during the challenge does not count against the target.

Only allowlisted devshard escrow creators can open a challenge. If the allowlist is empty, anyone can open one.

## Upgrade Plan

The node binary is upgraded through an on-chain software upgrade proposal. Existing hosts are not required to rebuild their `api` or `node` containers.

Devshard binaries stay on the versions already approved.

New hosts joining after the upgrade should use the `deploy/join` files in this PR.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. If the on-chain proposal is approved, this PR is merged immediately after the upgrade is executed on-chain.

## Migration

The handler is [`inference-chain/app/upgrades/v0_2_16/upgrades.go`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.16/inference-chain/app/upgrades/v0_2_16/upgrades.go). It applies these state changes.

- Extend existing cold-to-warm grants with PoC Challenge and model intent messages, so hosts do not have to repeat the ML ops grant.
- Move approved devshard versions from shared escrow params into a separate store without changing the approved list.
- Convert existing model scales to Dynamic Coefficients v1 and freeze the config for the upcoming epoch. Each model starts with its minimum and maximum equal to the current scale, so the upgrade does not change reward weights.
- Initialize PoC Challenge with a payment ratio of 0.1, at most 4 active challenges, and a minimum punishable segment of 300 blocks.

Fee groups stay disabled unless the software-upgrade proposal info explicitly enables and prices them.

## TODO

- [ ] Include additional simulation results and confirm the target percentage and coefficient range for each model in Dynamic Coefficients v1.

## Other Changes

### inference-chain

- Cap governance, BLS, and PoC voting power by previously confirmed compute. Rewards still use current weight. [#1588](https://github.com/gonka-ai/gonka/pull/1588), [#1694](https://github.com/gonka-ai/gonka/pull/1694), reworking [#1585](https://github.com/gonka-ai/gonka/pull/1585), by @libermans, @gmorgachev, @DimaOrekhovPS.
- Adjust model coefficients toward governance targets within configured bounds. [#1566](https://github.com/gonka-ai/gonka/pull/1566) by @gmorgachev.
- Add paid PoC challenges between regular PoCs. [#1811](https://github.com/gonka-ai/gonka/pull/1811) by @gmorgachev.
- Add fee groups and per-message gas rules, with charging disabled by default. [#1616](https://github.com/gonka-ai/gonka/pull/1616) by @GLiberman.
- Require participant permission for PoC v2 submissions and reject unauthorized authz wrappers. [#1623](https://github.com/gonka-ai/gonka/pull/1623), incorporating [#1552](https://github.com/gonka-ai/gonka/pull/1552), by @staaason, based on a HackerOne report.
- Reject malformed BLS encrypted shares and skip dealers whose shares cannot be decrypted. [#1687](https://github.com/gonka-ai/gonka/pull/1687) by @GLiberman.
- Reject SMST proofs that do not match the stored commitment. [#1782](https://github.com/gonka-ai/gonka/pull/1782) by @gmorgachev, based on a HackerOne report.
- Apply bootstrap non-participation penalties only to launching models and previous-epoch hosts. [#1740](https://github.com/gonka-ai/gonka/pull/1740) by @gmorgachev. Reported by @maksimenkoff.
- Seat epoch models from PoC results rather than hardware claims, and prevent activation of an unformed epoch. [7fb3d00](https://github.com/gonka-ai/gonka/commit/7fb3d00d9b) by @DimaOrekhovPS.
- Update approved devshard versions independently of other governance params. [#1741](https://github.com/gonka-ai/gonka/pull/1741) by @gmorgachev.
- Allow warm keys to declare model participation intent. Setting or refusing delegation still requires the cold key or a separate grant. [#1760](https://github.com/gonka-ai/gonka/pull/1760) by @DimaOrekhovPS.
- Prevent current-epoch statistics from being rebuilt after account settlement. [#1814](https://github.com/gonka-ai/gonka/pull/1814) by @gmorgachev.
- Expose epoch, claim recipient, performance, and vesting queries to Wasm contracts. [#1758](https://github.com/gonka-ai/gonka/pull/1758) by @niktverd, @DimaOrekhovPS.
- Return completed genesis transfers correctly from `TransferStatus`. [#1767](https://github.com/gonka-ai/gonka/pull/1767) by @zpoken, @DimaOrekhovPS.
- Sync Wasm contract sources with contracts already deployed on mainnet. [#1699](https://github.com/gonka-ai/gonka/pull/1699) by @GLiberman.

### decentralized-api

- Keep node removal from blocking PoC startup and ignore results from replaced nodes. [#1701](https://github.com/gonka-ai/gonka/pull/1701), incorporating [#1643](https://github.com/gonka-ai/gonka/pull/1643), by @GLiberman, @akup.
- Handle failed StoreCommit submissions and estimate transaction gas with a configurable multiplier. [#1639](https://github.com/gonka-ai/gonka/pull/1639) by @GLiberman, @DimaOrekhovPS.
- Include transaction admission checks in gas simulation. [#1638](https://github.com/gonka-ai/gonka/pull/1638) by @GLiberman.
- Retry failed payload pruning instead of permanently skipping epochs. [#1336](https://github.com/gonka-ai/gonka/pull/1336) by @redstartechno, @DimaOrekhovPS. Reported by @Mayveskii in [#850](https://github.com/gonka-ai/gonka/issues/850).
- Avoid starting queued ML-node work during worker shutdown. [#1727](https://github.com/gonka-ai/gonka/pull/1727) by @GLiberman.
- Restore JSON numbers and enum names in epoch, BLS, and participant responses. [#1752](https://github.com/gonka-ai/gonka/pull/1752), extracted from [#1526](https://github.com/gonka-ai/gonka/pull/1526), by @DimaOrekhovPS.
- Serve chain headers over HTTP and expose header and proof methods over NodeManager gRPC. [#1622](https://github.com/gonka-ai/gonka/pull/1622), [#1738](https://github.com/gonka-ai/gonka/pull/1738) by @akup.

### devshard / edge-api

- Redact the gateway private key from the devshardctl admin state response. [#1555](https://github.com/gonka-ai/gonka/pull/1555) by @aikuznetsov.
- Add chain-aware readiness and bounded request draining during edge-api shutdown, with matching compose settings. [#1605](https://github.com/gonka-ai/gonka/pull/1605) by @snevolin.

### mlnode / deploy

- Avoid the Kimi K2.6 prefill crash on B200 by selecting `TRTLLM_RAGGED`. [#1707](https://github.com/gonka-ai/gonka/pull/1707) by @vbgd0.
- Exclude nonces received after the measurement window from PoC benchmark throughput. [#1640](https://github.com/gonka-ai/gonka/pull/1640) by @vbgd0 (original fix), @baychak (port). Reported by @vbgd0.

### Documentation / tooling

- Add the [security model](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.16/docs/security-model.md), explaining useful work, Confirmation PoC, and PoC Challenge as security layers. [#1811](https://github.com/gonka-ai/gonka/pull/1811) by @gmorgachev.
- Document the release and upgrade process. [#1718](https://github.com/gonka-ai/gonka/pull/1718) by @gmorgachev.
- Align contribution and security reporting guidance with the bounty program and HackerOne. [#1693](https://github.com/gonka-ai/gonka/pull/1693) by @tcharchian.
- Update the README Discord invitation. [#1656](https://github.com/gonka-ai/gonka/pull/1656) by @tcharchian.
- Replace panics in the coefficient simulator with explicit errors to satisfy lint rules. [#1819](https://github.com/gonka-ai/gonka/pull/1819) by @GLiberman.

## Testing

The upgrade from v0.2.15 to v0.2.16 was rehearsed on a testnet using alpha builds. The software upgrade proposal executed at the proposed height, and the network resumed block production after the binary restart. All features included in this release were also tested separately.

Compatibility testing covered devshard v4.1 and v5 with both v0.2.15 and v0.2.16, verifying that both runtimes work before and after the upgrade.

## Contributors (sorted alphabetically)

- @aikuznetsov
- @akup
- @baychak
- @DimaOrekhovPS
- @GLiberman
- @gmorgachev
- @libermans
- @maksimenkoff
- @Mayveskii
- @niktverd
- @redstartechno
- @snevolin
- @staaason
- @tcharchian
- @vbgd0
- @zpoken
