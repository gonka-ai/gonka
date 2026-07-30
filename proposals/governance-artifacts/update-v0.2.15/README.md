# Upgrade Proposal: v0.2.15

This PR prepares the v0.2.15 release.

The mainnet chain/API work completes the classic inference API removal started in v0.2.14, rejects invalid bridge exchange transactions before they enter the mempool, fixes PoC voting on validator-side failures, and makes chain queries resilient to gRPC outages.

The release introduces services for high-availability deployments. Read-only chain API traffic can run through the independently scalable `edge-api` service. Devshard v4 can run multiple `versiond`/`devshardd` replicas behind `versiond-router` on shared Postgres.

## Upgrade Plan

The node binary is upgraded through an on-chain software upgrade proposal.

Devshard v4 is registered by the same proposal. The upgrade plan provides the v4 binary URL and sha256 hash. After the upgrade, `versiond` verifies and starts the binary under `/devshard/v4`. Earlier devshard versions remain available during the transition.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Before merge, the default `deploy/join` configuration is updated with the release image tags and the new `edge-api` and devshard routing topology.
3. If the on-chain proposal is approved, the chain upgrade executes at the proposed height and registers devshard v4.
4. This PR is merged immediately after the upgrade is executed on-chain.

## Changes

### inference-chain / decentralized-api

- Remove the deprecated classic inference transaction types and the remaining classic completions paths from decentralized-api [`a35a3f6`](https://github.com/gonka-ai/gonka/commit/a35a3f615a4504177c84e547604316c02c6235ef) by @gmorgachev, @DimaOrekhovPS.
- Validate bridge exchange transactions in the ante handler at CheckTx so invalid transactions are rejected before entering the mempool [#1498](https://github.com/gonka-ai/gonka/pull/1498) by @GLiberman.
- Abstain instead of voting -1 when PoC validation fails on the validator side, so a validator's own failures do not penalize honest participants [#1504](https://github.com/gonka-ai/gonka/pull/1504) by @gmorgachev.
- Add `edge-api` and optional `edge-api-router` services so read-only chain endpoints can scale independently from decentralized-api. The decentralized-api handlers remain available during the deployment transition [#1482](https://github.com/gonka-ai/gonka/pull/1482) by @akup.
- Fall back to CometBFT RPC when chain gRPC is unavailable for queries from `edge-api`, `devshardd`, and the devshard gateway. Transactions and streams remain on gRPC [#1505](https://github.com/gonka-ai/gonka/pull/1505) by @gmorgachev, @akup.

### devshard

- Add multiple `versiond`/`devshardd` replicas behind `versiond-router`, sticky session routing, shared Postgres storage, and exclusive validation leases [#1366](https://github.com/gonka-ai/gonka/pull/1366) by @akup, incorporating [#1126](https://github.com/gonka-ai/gonka/pull/1126), [#1144](https://github.com/gonka-ai/gonka/pull/1144), [#1152](https://github.com/gonka-ai/gonka/pull/1152), [#1154](https://github.com/gonka-ai/gonka/pull/1154) by @pixelplex.
- Add blue/green rolling updates for binaries published with a new sha256 under the same version name. New traffic moves to the updated child after its readiness checks pass, while accepted requests finish on the old child [#1490](https://github.com/gonka-ai/gonka/pull/1490) by @snevolin, @akup.
- Prevent observability requests from binding an escrow to a devshard version before the owner's first inference request [#1453](https://github.com/gonka-ai/gonka/pull/1453) by @akup.
- Move the devshard gateway from LCD REST to chain gRPC and confirm settlement transactions through DeliverTx [#1482](https://github.com/gonka-ai/gonka/pull/1482) by @akup.
- Add recovery for stale HA replicas and make diff persistence idempotent and persist-first [#1496](https://github.com/gonka-ai/gonka/pull/1496) by @akup.
- At settlement, credit the reserved cost of started inferences to the executor and refund pending inferences [#1368](https://github.com/gonka-ai/gonka/pull/1368) by @akup.
- Check signed accounting fields against the actual prompt workload and authorize execution from applied session state rather than an uncommitted request diff [#1444](https://github.com/gonka-ai/gonka/pull/1444), [#1461](https://github.com/gonka-ai/gonka/pull/1461) by @akup.
- Verify fetched host signatures before storing them and align devshard validation response checks with the mainnet policy [#1485](https://github.com/gonka-ai/gonka/pull/1485), [#1487](https://github.com/gonka-ai/gonka/pull/1487) by @0xMayoor, @akup.
- Return logprobs to clients that request them while continuing to remove internal validation fields [#1486](https://github.com/gonka-ai/gonka/pull/1486) by @redstartechno, @akup.
- Rebuild the Postgres session index asynchronously so a slow or stale escrow does not block all new sessions after restart [#1492](https://github.com/gonka-ai/gonka/pull/1492) by @akup.
- Add a Docker-backed devshard E2E test suite and expand versioned E2E coverage [#1484](https://github.com/gonka-ai/gonka/pull/1484), [#1295](https://github.com/gonka-ai/gonka/pull/1295), [#1332](https://github.com/gonka-ai/gonka/pull/1332) by @aikuznetsov, @akup.

## Testing

The devshard v4 runtime was tested on a pre-release testnet with multiple `versiond` replicas behind `versiond-router` on shared Postgres. Testing covered session routing, replica failover, validation leases, observability bind safety, `edge-api` routing, and same-name binary rolling updates.

The upgrade from v0.2.14 to v0.2.15 was rehearsed on a testnet using the alpha builds. The software upgrade proposal was submitted and executed at the proposed height, and the network came out healthy, with devshard inference traffic continuing through the upgrade window.

## Contributors (sorted alphabetically)

- @0xMayoor
- @aikuznetsov
- @akup
- @DimaOrekhovPS
- @GLiberman
- @gmorgachev
- @mtvnastya
- @pixelplex
- @redstartechno
- @Ryanchen911 (upgrade review)
- @snevolin
