# Phase 12 — production follow-up (tracked)

Index for post–testenv-v2 production consolidation. Each track has its own doc and
testenv exit criteria where applicable.

## Done

| Item | Location |
|------|----------|
| Long-poll **server** in `common/runtimeconfig` | Phase 1 |
| Chain **query** transport for params (`Params`/`EpochInfo`) → `common/chain` gRPC | Phase 2b |
| Long-poll **client** (gRPC loop + chain fallback + adaptive supervisor) | `common/runtimeconfig/client` |
| devshard re-exports | `devshard/runtimeconfig/alias.go` |

### gRPC-only gateway (Phase 12b) ✅

**Plan:** [`chain-transport-consolidation.md`](./chain-transport-consolidation.md)

LCD REST removed from `devshardctl`: escrow queries + create/settle tx use
`common/chain` gRPC (same transport as `devshardd`). Named scenarios **G1–G4** are
green in [`scenarios.md`](./scenarios.md).

| Track | Summary | Status |
|-------|---------|--------|
| A | `common/chain/tx` package | ✅ |
| B | mock-chain gRPC tx/auth face | ✅ |
| C | Gateway tx migration (drop `chain_tx_rest.go`) | ✅ |
| D | Gateway queries (`RESTBridge` → `GRPCBridge`) | ✅ |
| E | Compose/settings cleanup (`DEVSHARD_CHAIN_GRPC` only) | ✅ |
| F | Citest G1–G4 + REST removed gate | ✅ |

**Validation:**

```bash
make -C devshard/testenv citest-grpc-transport   # G1–G3 + G4 gate
make -C devshard/testenv citest-stack           # stack behavior regression
```

## Remaining (separate PRs)

These are **not** part of Phase 12b. Gateway gRPC consolidation is done; the work
below is production ownership of chainoracle and dapi’s older Cosmos client stack.

| Item | Status | Notes |
|------|--------|-------|
| **edge-api hosts chainoracle** | ❌ not landed | see below |
| **dapi → chainoracle client-only** | ❌ not landed | see below |
| **dapi `cosmosclient`/publisher → `common/chain`** | ❌ not landed | see below |

### edge-api hosts chainoracle — not landed

**Intent:** production edge-api should mount `devshard/chainoracle` (blocks HTTP/SSE +
params `GetRuntimeConfig` gRPC) so hosts and gateways can long-poll runtime config /
block proofs from the public edge path, not only from dapi.

**Current state:** `edge-api` talks to the chain with `common/chain` for its own Tier A
read APIs. It does **not** import or host `chainoracle`. Params long-poll and blocks
oracle still live on dapi / testenv `mock-dapi`.

**Why it still matters:** until edge-api hosts chainoracle, production params/blocks
serving stays coupled to dapi’s process, which blocks the “edge owns chain-facing
oracle” split.

### dapi → chainoracle client-only — not landed

**Intent:** after edge-api (or a sidecar) hosts chainoracle, dapi should stop owning the
oracle server and become a **client** of that surface (or drop the duplicate mount).

**Current state:** dapi still serves `GetRuntimeConfig` via `decentralized-api/nodemanager`
(using `common/runtimeconfig` under the hood). It is not “client-only” against an
external chainoracle mount.

**Depends on:** edge-api (or equivalent) hosting chainoracle first; otherwise there is
nowhere for dapi to point.

### dapi `cosmosclient` / publisher → `common/chain` — not landed

**Intent:** replace dapi’s Ignite/`cosmosclient` tx manager + publisher path with the
shared `common/chain` (+ `common/chain/tx`) stack already used by gateway and
devshardd.

**Current state:** `decentralized-api/cosmosclient` (and `tx_manager`) still own query/tx
broadcast for dapi. This is a larger migration than the gateway work: more message
types, batching/publisher behavior, and live mainnet callers.

**Scope:** out of scope for [`chain-transport-consolidation.md`](./chain-transport-consolidation.md).

## Tests (shipped)

Client extraction:

```bash
go test ./common/runtimeconfig/client/... -count=1
go test ./devshard/runtimeparams/... -count=1
```

Gateway gRPC transport:

```bash
make -C devshard/testenv citest-grpc-transport
```
