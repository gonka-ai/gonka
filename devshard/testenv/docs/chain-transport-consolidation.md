# Chain transport consolidation — gRPC-only gateway (Phase 12b)

**Goal:** Remove LCD REST and grpc-gateway HTTP from the **devshardctl (gateway)** chain
path. All chain **queries** and **tx** (create/settle escrow) go through `common/chain`
gRPC — the same transport devshardd already uses.

**Non-goals (separate tracks):**

- dapi `cosmosclient` / publisher → `common/chain` (merge-plan checklist; larger scope)
- edge-api / dapi chainoracle hosting (see `[phase12-followup.md](./phase12-followup.md)`)
- Removing CometBFT RPC `:26657` (devshardd events still need it)

**References:**


| Doc                                                         | Role                                                      |
| ----------------------------------------------------------- | --------------------------------------------------------- |
| `[phase12-followup.md](./phase12-followup.md)`              | Phase 12 index                                            |
| `[scenarios.md](./scenarios.md)`                            | Named stack scenarios shipped; **G1–G4** planned below    |
| `[testenv-v2-plan.md](./testenv-v2-plan.md)` § Phase 2b, 3c | Original transport decisions                              |
| `devshard/docs/merge-plan.md`                               | “Cosmos chain clients → common/”                          |


---

## Current state (audit)


| Consumer                                                         | Queries                                    | Tx broadcast                                             | Status          |
| ---------------------------------------------------------------- | ------------------------------------------ | -------------------------------------------------------- | --------------- |
| **devshardd** (`cmd/devshardd/bridge/chain.go`)                  | `common/chain.Client` gRPC                 | `cmd/devshardd/tx/manager.go` → `cosmos.tx.v1beta1` gRPC | ✅ gRPC          |
| **devshardctl params** (`runtime_params.go`)                     | `common/chain` gRPC primary; REST fallback | —                                                        | ⚠️ partial      |
| **devshardctl escrow reads** (`gateway.go`, `escrow_checker.go`) | `bridge.RESTBridge` → LCD JSON             | —                                                        | ❌ REST          |
| **devshardctl create/settle** (`chain_tx_rest.go`)               | account/tx poll via LCD                    | `POST /cosmos/tx/v1beta1/txs`                            | ❌ REST          |
| **testenv compose** (`gencompose`)                               | `DEVSHARD_CHAIN_GRPC`                      | `DEVSHARD_CHAIN_REST`, `DEVSHARD_TX_QUERY_REST`          | ❌ dual face     |
| **mock-chain**                                                   | gRPC `:9090` inference + cmtservice        | LCD `:1317` only (`restface`)                            | ⚠️ tx REST-only |


**Template to copy:** `devshard/cmd/devshardd/tx/manager.go` (gRPC `BroadcastTx`, auth `Account`
query) + `cmd/devshardd/bridge/chain.go` (gRPC escrow/participant queries).

---

## Target architecture

```text
devshardctl (gateway)
    │
    ├─ runtime params (adaptive)     → mock-dapi gRPC long-poll + common/chain gRPC fallback
    ├─ escrow / participant queries  → common/chain.Client (inference Query gRPC)
    └─ create / settle escrow        → common/chain/tx (cosmos.tx.v1beta1 gRPC)

mock-chain :9090
    ├─ inference Query  (existing grpcface)
    ├─ cmtservice       (existing grpcface)
    ├─ auth Query       (NEW — account number/sequence)
    └─ tx Service       (NEW — BroadcastTx, GetTx)

mock-chain :1317 LCD  → deprecated for gateway; remove after G3 green
```

**Settings after migration:**


| Removed                                              | Kept                                    |
| ---------------------------------------------------- | --------------------------------------- |
| `DEVSHARD_CHAIN_REST`, `chain_rest` in gateway store | `DEVSHARD_CHAIN_GRPC` / `NODE_GRPC_URL` |
| `DEVSHARD_TX_QUERY_REST`                             | `CHAIN_ID`, fee/gas env                 |
| `--chain-rest` flag                                  | `--chain-grpc`                          |


---

## Work breakdown (trackable)

Status key: ⬜ not started · 🟡 in progress · ✅ done

### Track A — `common/chain/tx` package


| ID  | Task                                                                                  | Owner / PR | Status |
| --- | ------------------------------------------------------------------------------------- | ---------- | ------ |
| A1  | Create `common/chain/tx/` — extract from `devshard/cmd/devshardd/tx/manager.go`       | PR-1       | ✅      |
| A2  | `Manager.BroadcastTx` (sync), `GetTx` wait, account number/sequence via auth gRPC     | PR-1       | ✅      |
| A3  | `CreateDevshardEscrow`, `SettleDevshardEscrow` helpers (accept signer interface)      | PR-1       | ✅      |
| A4  | Signer adapter for `devshard/signing.Secp256k1Signer` (gateway) + keyring (devshardd) | PR-1       | ✅      |
| A5  | Unit tests with in-process gRPC (no LCD)                                              | PR-1       | ✅      |
| A6  | devshardd `tx/manager.go` becomes thin wrapper over `common/chain/tx`                 | PR-2       | ✅      |


**Exit A:** `go test ./common/chain/tx/...` green; devshardd dispute tx unchanged.

### Track B — mock-chain gRPC tx face


| ID  | Task                                                                        | Owner / PR | Status |
| --- | --------------------------------------------------------------------------- | ---------- | ------ |
| B1  | Register `cosmos.auth.v1beta1` Query (account by address) on `grpcface`     | PR-2       | ✅      |
| B2  | Register `cosmos.tx.v1beta1` Service (`BroadcastTx`, `GetTx`) on `grpcface` | PR-2       | ✅      |
| B3  | Reuse `restface/txdecode.go` + `txexec.go` logic for gRPC tx body decode    | PR-2       | ✅      |
| B4  | Emit CometBFT `Tx` events on gRPC broadcast (parity with REST 3c)           | PR-2       | ✅      |
| B5  | Unit tests: gRPC create escrow → `DevshardEscrow` query; no REST listener   | PR-2       | ✅      |


**Exit B:** `go test ./devshard/testenv/mockchain/grpcface/...` includes tx round-trip.

### Track C — gateway tx migration


| ID  | Task                                                                                                        | Owner / PR | Status |
| --- | ----------------------------------------------------------------------------------------------------------- | ---------- | ------ |
| C1  | Replace `RESTChainTxClient` with `common/chain/tx` in `escrow_rotator.go`, `gateway.go`                     | PR-3       | ✅      |
| C2  | Port `chain_tx_rest_mockchain_test.go` → gRPC-only (`TestGRPCChainTxClient_CreateDevshardEscrow_MockChain`) | PR-3       | ✅      |
| C3  | Delete `chain_tx_rest.go` + `chain_tx_rest_test.go` when C2 + G2 green                                      | PR-4       | ✅      |
| C4  | Remove `DEVSHARD_TX_QUERY_REST` from gencompose + gateway wiring tests                                      | PR-4       | ✅      |


**Exit C:** No `NewRESTChainTxClient` in `devshard/cmd/devshardctl`.

### Track D — gateway query migration (`RESTBridge` → gRPC)


| ID  | Task                                                                                                              | Owner / PR | Status |
| --- | ----------------------------------------------------------------------------------------------------------------- | ---------- | ------ |
| D1  | Add `bridge.GRPCBridge` (or wire gateway to `cmd/devshardd/bridge/chain.go` patterns) using `common/chain.Client` | PR-3       | ✅      |
| D2  | Migrate `gateway.go` `NewRESTBridge` → gRPC bridge                                                                | PR-3       | ✅      |
| D3  | Migrate `escrow_checker.go`                                                                                       | PR-3       | ✅      |
| D4  | Phase gate: stop using `ChainREST` for preserved snapshot base URL                                                | PR-3       | ✅      |
| D5  | Port `bridge/rest_test.go` cases to gRPC bridge tests                                                             | PR-3       | ✅      |
| D6  | Delete `bridge/rest.go` when unused (keep until deploy/join REST consumers gone)                                  | PR-5       | ⬜      |


**Exit D:** Gateway escrow read path uses gRPC only.

### Track E — settings & compose cleanup


| ID  | Task                                                                                    | Owner / PR | Status |
| --- | --------------------------------------------------------------------------------------- | ---------- | ------ |
| E1  | gencompose: drop `DEVSHARD_CHAIN_REST` from devshardctl service                         | PR-4       | ⬜      |
| E2  | `runtime_params.go`: remove REST chain fetcher fallback (or gate behind deprecated env) | PR-4       | ⬜      |
| E3  | Gateway persisted settings: migrate `chain_rest` → deprecated/no-op                     | PR-4       | ⬜      |
| E4  | Update `DEVELOPMENT-MODE.md`, `testenv-v2.md`, deploy/join docs                         | PR-4       | ⬜      |
| E5  | Optional: stop publishing mock-chain `:1317` in compose (after G3)                      | PR-5       | ⬜      |


**Exit E:** `docker compose config` for testenv has no gateway REST chain env vars.

### Track F — testenv citest scenarios


| ID     | Scenario           | Validates                                                                      | Test (planned)                                 | Status |
| ------ | ------------------ | ------------------------------------------------------------------------------ | ---------------------------------------------- | ------ |
| **G1** | gRPC escrow create | Gateway creates escrow via gRPC tx only; visible on gRPC query                 | `TestG1_GatewayEscrowCreateGRPC`               | ✅      |
| **G2** | gRPC escrow read   | Gateway reads escrow via gRPC bridge (no LCD)                                  | `TestG2_GatewayEscrowReadGRPC`                 | ✅      |
| **G3** | Chat without LCD   | Full chat path with compose **without** `DEVSHARD_CHAIN_REST` / mock REST port | `TestG3_GatewayChatGRPCOnly`                   | ⬜      |
| **G4** | REST removed gate  | CI grep: no `NewRESTBridge` / `RESTChainTxClient` in devshardctl               | `TestG4_NoRESTChainClientsInGatewayProduction` | ✅      |


Detail: `[scenarios.md](./scenarios.md)` § Phase 12 transport scenarios.

**Makefile target (add in PR-4):**

```makefile
citest-grpc-transport:  ## G1–G3 gRPC-only gateway citest
	TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run 'TestG1_|TestG2_|TestG3_' -count=1 -v -timeout 30m
```

Fold **G3** into `make citest-stack` once `TestGatewayChat` is gRPC-only.

---

## Recommended PR order

```text
PR-1  common/chain/tx + unit tests                          (A1–A5)
PR-2  mock-chain gRPC tx/auth face + devshardd wrapper       (B1–B5, A6)
PR-3  gateway tx + query migration + unit tests              (C1–C2, D1–D5)
PR-4  G1–G3 citest + compose/settings cleanup                (C4, E1–E4, F)
PR-5  delete REST code + optional mock-chain 3c removal      (C3, D6, E5)
```

Each PR must keep the existing stack behavior suite green until PR-4 switches
gateway chat to gRPC-only.

---

## Test matrix


| Layer                  | Command                                                      | When     |
| ---------------------- | ------------------------------------------------------------ | -------- |
| `common/chain/tx` unit | `go test ./common/chain/tx/... -count=1`                     | PR-1+    |
| mock-chain gRPC tx     | `go test ./devshard/testenv/mockchain/grpcface/... -count=1` | PR-2+    |
| gateway unit (no REST) | `go test ./devshard/cmd/devshardctl/... -count=1`            | PR-3+    |
| bridge gRPC            | `go test ./devshard/bridge/... -count=1`                     | PR-3+    |
| G1–G3 citest           | `make citest-grpc-transport`                                 | PR-4+    |
| Full stack regression  | `make citest-stack`                                          | every PR |
| Transport gate         | `go test ./devshard/testenv/citest/ -run TestG4 -count=1`    | PR-4+    |
| CI unit                | `make -C devshard ci-testenv-unit`                           | every PR |


---

## Signer strategy (decision)


| Caller                | Today                                                    | Target                                        |
| --------------------- | -------------------------------------------------------- | --------------------------------------------- |
| devshardd dispute tx  | Cosmos file keyring                                      | `common/chain/tx` + keyring signer            |
| gateway create/settle | `devshard/signing.Secp256k1Signer` (hex key in settings) | `common/chain/tx` + `Secp256k1Signer` adapter |


Do **not** force gateway onto file keyring in v1 — adapter keeps gateway deploy model unchanged.

---

## Exit criteria (definition of done)

- [ ] `devshardctl` has **zero** production chain `http.Client` calls (excluding `DEVSHARD_PUBLIC_API` → mock-dapi).
- [ ] `grep -r 'NewRESTBridge\|RESTChainTxClient\|DEVSHARD_CHAIN_REST' devshard/cmd/devshardctl` → empty.
- [ ] gencompose devshardctl service: `DEVSHARD_CHAIN_GRPC` only (no REST chain env).
- [ ] **G1, G2, G3** citest green; gateway chat runs through G3 / `citest-stack` without LCD.
- [ ] `make -C devshard ci-testenv-unit` and `ci-testenv-integration` green.
- [ ] `[scenarios.md](./scenarios.md)` updated: G1–G3 marked ✅ and gateway chat no longer mentions LCD.

---

## Risks


| Risk                                   | Mitigation                                                   |
| -------------------------------------- | ------------------------------------------------------------ |
| mock-chain gRPC tx parity with REST 3c | B4 event emission; port existing `restface` tx tests to gRPC |
| Gateway signer ≠ keyring               | Signer interface in `common/chain/tx` (see above)            |
| deploy/join still documents REST       | E4 doc pass; coordinate with ops before E5                   |
| Gateway chat regression during migration | Keep LCD until G3 passes; dual-path one release optional   |


---

## Progress log


| Date       | PR  | Notes                                                                        |
| ---------- | --- | ---------------------------------------------------------------------------- |
| 2026-06-24 | —   | Track C + D: gateway gRPC tx/query, G2/G4 tests, gencompose REST env removed |
| 2026-06-24 | —   | Track A + B: `common/chain/tx`, mock-chain gRPC auth/tx, G1 citest           |

