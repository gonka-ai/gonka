# Chain transport consolidation — gRPC-only gateway (Phase 12b)

**Status:** ✅ Done (gateway path). Leftovers below are compatibility / naming only.

**Goal:** Remove LCD REST and grpc-gateway HTTP from the **devshardctl (gateway)** chain
path. All chain **queries** and **tx** (create/settle escrow) go through `common/chain`
gRPC — the same transport devshardd already uses.

**Non-goals (separate tracks):**

- dapi `cosmosclient` / publisher → `common/chain` (larger scope; see [`phase12-followup.md`](./phase12-followup.md))
- edge-api / dapi chainoracle hosting (see [`phase12-followup.md`](./phase12-followup.md))
- Removing CometBFT RPC `:26657` (devshardd events still need it)

**References:**

| Doc | Role |
| --- | --- |
| [`phase12-followup.md`](./phase12-followup.md) | Phase 12 index |
| [`scenarios.md`](./scenarios.md) | Named stack scenarios; **G1–G4** ✅ |
| [`testenv-v2-plan.md`](./testenv-v2-plan.md) § Phase 2b, 3c | Original transport decisions |

---

## Current state (audit)

| Consumer | Queries | Tx broadcast | Status |
| --- | --- | --- | --- |
| **devshardd** (`cmd/devshardd/bridge/chain.go`) | `common/chain.Client` gRPC | `common/chain/tx` → `cosmos.tx.v1beta1` gRPC | ✅ gRPC |
| **devshardctl params** (`runtime_params.go`) | `NewGRPCChainFetcher` on shared `common/chain.Client` | — | ✅ gRPC |
| **devshardctl escrow reads** (`gateway.go`, `escrow_checker.go`) | `bridge.GRPCBridge` | — | ✅ gRPC |
| **devshardctl create/settle** | — | `common/chain/tx` | ✅ gRPC |
| **testenv compose** (`gencompose`) | `DEVSHARD_CHAIN_GRPC` only | same | ✅ gRPC-only |
| **mock-chain** | gRPC `:9090` (inference, cmtservice, auth, tx) | gRPC `BroadcastTx` / `GetTx` | ✅ gRPC (`restface` removed) |

---

## Architecture (landed)

```text
devshardctl (gateway)
    │
    ├─ runtime params (adaptive)     → mock-dapi gRPC long-poll + common/chain gRPC fallback
    ├─ escrow / participant queries  → bridge.GRPCBridge → common/chain.Client
    └─ create / settle escrow        → common/chain/tx (cosmos.tx.v1beta1 gRPC)

mock-chain :9090
    ├─ inference Query
    ├─ cmtservice
    ├─ auth Query       (account number/sequence)
    └─ tx Service       (BroadcastTx, GetTx)

mock-chain LCD :1317  → removed from testenv (no restface / no compose port)
```

**Settings:**

| Removed / ignored | Kept |
| --- | --- |
| `DEVSHARD_CHAIN_REST`, `--chain-rest` | `DEVSHARD_CHAIN_GRPC` / `NODE_GRPC_URL` |
| `DEVSHARD_TX_QUERY_REST` | `CHAIN_ID`, fee/gas env |
| `GatewaySettings.ChainREST` / admin `chain_rest` | `--chain-grpc`, `chain_grpc` |
| SQLite `chain_rest` column | dropped on store open |

---

## Work breakdown

Status key: ⬜ not started · 🟡 in progress · ✅ done

### Track A — `common/chain/tx` package ✅

| ID | Task | Status |
| --- | --- | --- |
| A1 | Create `common/chain/tx/` — extract from `devshard/cmd/devshardd/tx/manager.go` | ✅ |
| A2 | `Manager.BroadcastTx` (sync), `GetTx` wait, account number/sequence via auth gRPC | ✅ |
| A3 | `CreateDevshardEscrow`, `SettleDevshardEscrow` helpers (accept signer interface) | ✅ |
| A4 | Signer adapter for `devshard/signing.Secp256k1Signer` (gateway) + keyring (devshardd) | ✅ |
| A5 | Unit tests with in-process gRPC (no LCD) | ✅ |
| A6 | devshardd `tx/manager.go` becomes thin wrapper over `common/chain/tx` | ✅ |

### Track B — mock-chain gRPC tx face ✅

| ID | Task | Status |
| --- | --- | --- |
| B1 | Register `cosmos.auth.v1beta1` Query on `grpcface` | ✅ |
| B2 | Register `cosmos.tx.v1beta1` Service (`BroadcastTx`, `GetTx`) on `grpcface` | ✅ |
| B3 | Reuse tx decode/exec logic for gRPC tx body | ✅ |
| B4 | Emit CometBFT `Tx` events on gRPC broadcast | ✅ |
| B5 | Unit tests: gRPC create escrow → `DevshardEscrow` query; no REST listener | ✅ |

### Track C — gateway tx migration ✅

| ID | Task | Status |
| --- | --- | --- |
| C1 | Replace `RESTChainTxClient` with `common/chain/tx` | ✅ |
| C2 | gRPC-only mock-chain create-escrow test | ✅ |
| C3 | Delete `chain_tx_rest.go` + tests | ✅ |
| C4 | Remove `DEVSHARD_TX_QUERY_REST` from gencompose + wiring tests | ✅ |

### Track D — gateway query migration (`RESTBridge` → gRPC) ✅

| ID | Task | Status |
| --- | --- | --- |
| D1 | `bridge.GRPCBridge` on `common/chain.Client` | ✅ |
| D2 | `gateway.go` uses gRPC bridge | ✅ |
| D3 | `escrow_checker.go` uses gRPC bridge | ✅ |
| D4 | Phase gate no longer uses `ChainREST` as chain base URL | ✅ |
| D5 | Bridge tests cover gRPC path | ✅ |
| D6 | Delete `bridge/rest.go` | ✅ |

### Track E — settings & compose cleanup ✅

| ID | Task | Status |
| --- | --- | --- |
| E1 | gencompose: drop `DEVSHARD_CHAIN_REST` from devshardctl | ✅ |
| E2 | `runtime_params.go`: gRPC chain fetcher only | ✅ |
| E3 | Persisted `chain_rest` removed from settings/API; SQLite column dropped on open | ✅ |
| E4 | `DEVELOPMENT-MODE.md`, deploy/join env template document gRPC-only | ✅ |
| E5 | mock-chain `:1317` / `restface` removed from testenv | ✅ |

### Track F — testenv citest scenarios ✅

| ID | Scenario | Test | Status |
| --- | --- | --- | --- |
| **G1** | gRPC escrow create | `TestGatewayEscrowCreateGRPC` | ✅ |
| **G2** | gRPC escrow read | `TestGatewayEscrowReadGRPC` | ✅ |
| **G3** | Chat without LCD | `TestGatewayChatGRPCOnly` | ✅ |
| **G4** | REST removed gate | `TestNoRESTChainClientsInGatewayProduction` | ✅ |

Detail: [`scenarios.md`](./scenarios.md) § Phase 12 transport scenarios.

```makefile
citest-grpc-transport:  ## G1–G4 gRPC-only gateway citest
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestGatewayEscrowCreateGRPC|TestGatewayEscrowReadGRPC|TestGatewayChatGRPCOnly' -count=1 -v -timeout 30m
	cd .. && go test ./cmd/devshardctl/ -run TestNoRESTChainClientsInGatewayProduction -count=1 -v
```

---

## Post-cleanup notes

| Item | Resolution |
| --- | --- |
| `GatewaySettings.ChainREST` | Removed from settings/admin API. SQLite `chain_rest` column is dropped on store open. |
| Legacy `TestNewRESTBridge…` | Removed; coverage lives in `TestEscrowCheckerUsesBridgeGetEscrow`. |
| `http.Client` in gateway | Kept for `DEVSHARD_PUBLIC_API` / phase gate / versions cache only (documented on those types). Not chain LCD. |
| `transport_gate_test.go` | Still lists forbidden REST chain symbols; that file is excluded from the scan. |

---

## Test matrix

| Layer | Command |
| --- | --- |
| `common/chain/tx` unit | `go test ./common/chain/tx/... -count=1` |
| mock-chain gRPC tx | `go test ./devshard/testenv/mockchain/grpcface/... -count=1` |
| gateway unit | `go test ./devshard/cmd/devshardctl/... -count=1` |
| bridge gRPC | `go test ./devshard/bridge/... -count=1` |
| G1–G4 citest | `make -C devshard/testenv citest-grpc-transport` |
| Full stack regression | `make -C devshard/testenv citest-stack` |
| CI unit | `make -C devshard ci-testenv-unit` |

---

## Signer strategy (landed)

| Caller | Implementation |
| --- | --- |
| devshardd dispute tx | `common/chain/tx` + Cosmos file keyring signer |
| gateway create/settle | `common/chain/tx` + `Secp256k1Signer` adapter |

Gateway was **not** forced onto file keyring — adapter keeps the hex-key deploy model.

---

## Exit criteria (definition of done)

- [x] `devshardctl` has **zero** production **chain** `http.Client` calls (non-chain HTTP for `DEVSHARD_PUBLIC_API` / phase gate / versions cache is allowed).
- [x] No production `NewRESTBridge` / `RESTChainTxClient` / `DEVSHARD_CHAIN_REST` usage in `devshard/cmd/devshardctl` (`TestNoRESTChainClientsInGatewayProduction`).
- [x] gencompose devshardctl service: `DEVSHARD_CHAIN_GRPC` only (no REST chain env).
- [x] **G1–G4** citest green; gateway chat runs without LCD.
- [x] `make -C devshard ci-testenv-unit` / integration targets cover the gRPC path.
- [x] [`scenarios.md`](./scenarios.md): G1–G4 marked ✅; gateway chat no longer requires LCD.

---

## Risks (historical — mitigated)

| Risk | Mitigation |
| --- | --- |
| mock-chain gRPC tx parity with old REST 3c | B4 event emission; gRPC tx tests |
| Gateway signer ≠ keyring | Signer interface in `common/chain/tx` |
| deploy/join still documents REST | E4; join template uses `DEVSHARD_CHAIN_GRPC` |
| Gateway chat regression during migration | G3 citest; LCD removed after green |

---

## Progress log

| Date | Notes |
| --- | --- |
| 2026-06-24 | Track A + B: `common/chain/tx`, mock-chain gRPC auth/tx, G1 |
| 2026-06-24 | Track C + D: gateway gRPC tx/query, G2/G4, gencompose REST env removed |
| 2026-08-06 | Doc sync: all tracks A–F ✅; leftovers documented; `merge-plan.md` reference removed |
| 2026-08-06 | Cleared leftovers: drop `ChainREST` API/settings field, remove legacy REST test name |
