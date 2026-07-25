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

## Active — gRPC-only gateway (Phase 12b)

**Plan:** [`chain-transport-consolidation.md`](./chain-transport-consolidation.md)

Remove LCD REST from devshardctl: escrow queries + create/settle tx → `common/chain`
gRPC (same as devshardd). Testenv scenarios **G1–G3**; see [`scenarios.md`](./scenarios.md).

| Track | Summary | Status |
|-------|---------|--------|
| A | `common/chain/tx` package | ⬜ |
| B | mock-chain gRPC tx/auth face | ⬜ |
| C | Gateway tx migration (drop `chain_tx_rest.go`) | ✅ |
| D | Gateway queries (`RESTBridge` → gRPC) | ⬜ |
| E | Compose/settings cleanup | ⬜ |
| F | Citest G1–G4 | ⬜ |

**Quick validation (when implemented):**

```bash
make -C devshard/testenv citest-grpc-transport   # G1–G3
make -C devshard/testenv citest-stack           # stack behavior regression
```

## Remaining (separate PRs)

| Item | Notes | Plan |
|------|-------|------|
| **edge-api hosts chainoracle** | blocks HTTP + params gRPC in prod | TBD |
| **dapi → chainoracle client-only** | sidecar mounts `chainoracle/params` | TBD |
| **dapi `cosmosclient`/publisher → `common/chain`** | tx + publisher; larger than gateway | `merge-plan.md` checklist |

dapi consolidation is **out of scope** for [`chain-transport-consolidation.md`](./chain-transport-consolidation.md).

## Tests (shipped)

Client extraction:

```bash
go test ./common/runtimeconfig/client/... -count=1
go test ./devshard/runtimeparams/... -count=1
```
