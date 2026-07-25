# Upgrade Proposal: v0.2.13-devshard-v2

This proposal covers the devshard v2 release.

This is the first devshard-only upgrade. It operates independently of full chain software upgrades. Once approved, v2 runs in parallel with the existing v1 devshard runtime.

See the [upgrade design doc](https://github.com/gonka-ai/gonka/blob/devshard-0.2.13-v2/devshard/docs/upgrade.md) and the [versioned](https://github.com/gonka-ai/gonka/tree/devshard-0.2.13-v2/versioned) package for details.

## Upgrade Plan

The devshard runtime is upgraded through an on-chain params proposal, not a full chain software upgrade.

The proposal registers a new entry in `DevshardEscrowParams.approved_versions`:

- `name`: `v2`
- `binary`: `URL`
- `sha256`: `SHA256`

The release publishes the `devshardd` binary as a Gonka release artifact. If the on-chain proposal is approved, `versiond` automatically downloads the binary, verifies the sha256 hash, and starts an additional `devshardd` process inside the existing `versiond` container.

The new process is served under the `/devshard/v2` prefix. Existing v1 devshard traffic continues on `/devshard/v1` and `/v1/devshard`. No mainnet restart or manual host steps are expected during this type of upgrade.

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Release the `devshardd` binary with the `URL` and `SHA256` values above.
3. Submit the on-chain params proposal.
4. If the on-chain proposal is approved, this PR will be merged immediately after the proposal executes on-chain.

## Testing

On testnet running v0.2.13 with working v1 devshard inference, a governance proposal registering v2 was submitted and approved. After approval, inference requests succeeded on both `/devshard/v1` and `/devshard/v2`.

## Changes

### devshard

- Prune old epoch storage on epoch changes, move SQLite/Postgres schema setup out of hot paths, and select exactly one storage backend per process
- Remove the seed reveal round, seal completed inference stats, and prune payloads so long-running sessions do not keep all served inferences in RAM or state
- Re-gossip stale `MsgFinishInference` transactions so the sequencer can pick them up from another host's mempool
- Enforce the governance-controlled maximum nonce limit on hosts to reject invalid requests before settlement
- Separate devshard runtime version from state-root protocol version and stamp protocol v2 at build time
- Create sessions from on-chain escrow fee snapshots and runtime config instead of hardcoded values (with direct chain fallback until mainnet has the matching NodeManager runtime-config endpoint)
- Store per-inference validation counters outside the state root in SQLite/Postgres and expose per-slot totals through devshard stats endpoints after inference pruning
- Add internal devshard traces and metrics through OpenTelemetry and Prometheus
- Return typed devshard errors for disabled, initializing, and non-retryable states instead of generic failures

### decentralized-api

The changes in the `decentralized-api/` module are fully backward compatible and do not need to be activated before the next mainnet release.

- Serve chain-backed devshard runtime config through the NodeManager `GetRuntimeConfig` gRPC long-poll
- Add dapi traces and metrics for public inference requests, event listening, validation, chain queries, transaction broadcasts, and ML node calls
- Propagate trace context across executor forwarding, validation payload fetches, and ML node calls

### inference-chain

The changes in the `inference-chain/` module are wire-compatible and do not need to be activated before the next mainnet release.

- Rename the `version` field to `state_root_and_protocol_version` in the devshard settlement message proto
- Move devshard session timeouts, fees, validation rates, vote threshold factor, and grace periods to governance-controlled `DevshardEscrowParams`
- Add `create_devshard_fee` and `fee_per_nonce` to `DevshardEscrow` to snapshot active fees at escrow creation

### deploy

- Add join-stack observability with Grafana, Jaeger, Prometheus, Loki, Promtail, and cAdvisor
- Add dashboards for devshard sessions, chain health, query latency, storage, containers, and node health
