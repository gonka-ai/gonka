# Inference During the PoC Validation Phase

This document describes how the Gonka network keeps serving inference during the PoC validation stage. It builds on the Proof of Compute design in [gonka_poc.md](gonka_poc.md), where a PoC sprint normally takes most nodes away from inference and only preserved nodes keep answering requests.

## Background

PoC version 2 runs the proof computation inside the vLLM process instead of a separate worker. During the validation stage a node re-runs a sampled set of nonces rather than generating new ones, so most of the GPU stays idle. A node in that state can still answer inference requests while it validates.

The earlier behavior treated the whole sprint the same way: only preserved nodes served inference, through both generation and validation, and the gateway raised its per-weight concurrency limit for the entire sprint. Two facts let us do better during validation. The work is light, and each node knows whether its vLLM build allows inference while validation runs. Three changes use that:

1. ML nodes report, per node, whether they can serve inference during validation.
2. The gateway routes inference to those capable nodes during the validation phase, on top of the preserved set.
3. The raised concurrency limit applies only during the generation phase, not validation.

## Capability Reporting

The capability travels from the ML node to the developer-facing gateway over the existing version endpoints.

### ML Node Side

**vLLM (`vllm/.../poc/routes.py`)**:
- The `/api/v1/pow/versions` route returns the build version and a `poc_validation_inference` flag.
- The flag is `true` when the build can answer inference requests while PoC validation runs in the same process.

**MLNode API (`mlnode/packages/api/src/api/routes.py`)**:
- The `/api/v1/state` route includes `poc_validation_inference` alongside the existing node heartbeat fields, so the broker learns capability without an extra per-node polling request.
- The `/api/v1/versions` route is also available on `poc_port` for direct capability inspection.
- Both routes use vLLM's `/api/v1/pow/versions` response from a healthy backend. The first successful response is cached because it is a build capability. Unknown capability, no healthy backend, and vLLM errors fail closed to `poc_validation_inference: false`.

### Decentralized API Side

**Per-node tracking (`decentralized-api/broker/broker.go`)**:
- `queryNodeStatus` reads `poc_validation_inference` from the regular `mlnodeclient.NodeState` response. Older MLNodes omit the field, which decodes to `false`.
- The result is stored on `NodeState.PoCValidationInference`. A failed state request also leaves the flag false.
- The flag flows through the same path as the node version: `statusQueryResult` to `StatusUpdate` to `NodeState`. The reconcile step emits an update when only this flag changes, so a flip is not held back until an unrelated status change.

**Public reporting (`decentralized-api/internal/server/public/app_info_handlers.go`)**:
- `GET /v1/versions` returns a per-node `mlnodes` list. Each entry carries `node_id`, `version`, and `poc_validation_inference`, built from `Broker.GetNodes()`.
- A nil broker yields an empty list. A `GetNodes` error is logged and also yields an empty list; the endpoint still returns the API and node versions.

The flag is self-reported and used only for routing. It never gates authorization or consensus.

## Gateway Routing During Validation

The devshard gateway decides, per phase, which miners can take inference traffic. The phase constants live in `devshard/cmd/devshardctl/phase_gate.go`.

**Generation phases** (`PoCGenerate`, `PoCGenerateWindDown`, and the confirmation-PoC grace and generation phases): only preserved miners serve inference, as before.

**Validation phases** (`PoCValidate`, `PoCValidateWindDown`, `CONFIRMATION_POC_VALIDATION`): preserved miners plus any non-preserved miner that has at least one validation-capable node.

### Tracking Capability (`versions_cache.go`)

- `VersionsCache` runs as a background poller on the gate's lifecycle. It reads each candidate miner's `/v1/versions` and records, per `(miner, node_id)`, whether that node reports `poc_validation_inference`.
- `IsNodeValidationCapable(miner, nodeID)` answers the per-node question. It returns `false` for an unknown miner or node, a failed fetch, or an entry older than the cache TTL.
- The poller holds its lock only to copy the candidate set and to store results. It does not hold the lock across the HTTP call, so a slow miner does not block reads from the refresh path.
- `refresh` feeds the candidate set from the participants response on every poll, so the cache stays warm and is ready when the phase turns to validation.

### Merging the Sets (`phase_gate.go`)

During a validation phase, `mergePreservedWithValidationCapable` produces the availability and weight views the limiter uses:

- Preserved miners keep their PoC-filtered weight.
- For each non-preserved miner, the gateway sums the chain weight of only its validation-capable nodes, per model, and adds the miner with that weight.
- A miner with no capable node is left out.

The per-node weight comes from the chain participants response (`chainMLNodeInfo`, fields `node_id` and `poc_weight`). The `node_id` in the `/v1/versions` list is the miner's broker `Node.Id`, which is the same identifier the chain stores as `MLNodeInfo.NodeId` (set in `decentralized-api/broker/broker.go` when the node is registered). The gateway matches the two by `node_id` and sums.

Giving a capable miner real capacity needs both halves. `CapacityState.TotalWeightForModel` sums current weight only over available hosts, so the merge adds the miner to the preserved set and writes its capable-node weight into the current view. The full view stays at the steady-state weight.

## Concurrency Limit

The gateway scales its concurrency cap by `PoCMaxConcurrentPer10000Weight` when fewer nodes serve inference. That happens during generation, when only preserved nodes are available. Validation reopens the non-preserved capable nodes, so the higher cap is no longer needed there.

**Phase-scoped cap (`devshard/cmd/devshardctl/gateway.go`)**:
- `currentMaxConcurrentPer10000Weight` returns `PoCMaxConcurrentPer10000Weight` only when `pocGenerationActive` is true.
- `pocGenerationActive` reads the phase snapshot and calls `rawPoCGenerationState`, which is true for the four generation phases and false for validation.
- Validation and steady-state both fall back to the base `MaxConcurrentPer10000Weight`.

## Fail-Closed Behavior

Every unknown resolves to "not capable", so a miner or node is never offered traffic it cannot serve:

- A node whose `/api/v1/state` response does not include `poc_validation_inference` reports `false`.
- A miner whose `/v1/versions` cannot be reached or parsed contributes no capable nodes.
- A cache entry past its TTL is treated as unknown until the next poll refreshes it.
- A nil broker or a missing node returns `false`.

## Key Implementation Files

### ML Node
- `vllm/.../poc/routes.py` - the vLLM `/api/v1/pow/versions` route and the `poc_validation_inference` flag
- `mlnode/packages/api/src/api/routes.py` - the MLNode `/api/v1/state` and `/api/v1/versions` endpoints that proxy vLLM capability

### Decentralized API
- `decentralized-api/mlnodeclient/client.go` - `StateResponse` with `poc_validation_inference`
- `decentralized-api/broker/broker.go` - per-node capability in `queryNodeStatus` and `NodeState`
- `decentralized-api/internal/server/public/app_info_handlers.go` - the per-node `mlnodes` list on `/v1/versions`

### Devshard Gateway
- `devshard/cmd/devshardctl/versions_cache.go` - the background capability poller
- `devshard/cmd/devshardctl/phase_gate.go` - `mergePreservedWithValidationCapable`, `rawPoCValidationState`, `rawPoCGenerationState`
- `devshard/cmd/devshardctl/gateway.go` - the phase-scoped concurrency cap

## Summary

During PoC validation a node does light work, so it can answer inference alongside it. Each node reports whether its build allows that, the gateway learns it through `/v1/versions`, and during the validation phase the gateway routes inference to preserved miners plus the non-preserved miners whose nodes are capable, weighted by those nodes. The raised concurrency cap stays scoped to generation, where the serving set is smallest. Unknown capability always reads as not capable, so the change only ever adds traffic to nodes that have said they can take it.
