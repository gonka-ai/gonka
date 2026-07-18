# Stack citest scenarios (S1–S8)

Implemented Go integration tests for the devshard testenv v2 stack. Each scenario
boots a real Docker Compose stack (mock-chain, mock-dapi, mock-openai, versiond × 2,
versiond-router, devshardctl, Postgres) and asserts production-like behaviour end to end.

**Design history:** [`testenv-v2-plan.md`](./testenv-v2-plan.md) (planning doc; scenarios below are what shipped).

## Stack under test

| Service | Role in tests |
|---------|----------------|
| **mock-chain** | Cosmos gRPC `:9090`, CometBFT RPC `:26657`, admin `/testenv/*` |
| **mock-dapi** | NodeManager gRPC (`GetRuntimeConfig` long-poll), chainoracle HTTP, fault proxy |
| **mock-openai** | OpenAI-compatible ML upstream for devshardd after `AcquireMLNode` |
| **versiond-0 / versiond-1** | Supervise linux **devshardd** child (protocol `v2`); both load the **same** `KEY_NAME` (HA participant `hosts[0]`) |
| **versiond-router** | Sticky nginx (`consistent_hash` on session id) |
| **devshardctl** | Gateway (`/v1/chat/completions`, `/v1/status`) |
| **devshard-postgres** | Shared payload store (required for 2× versiond) |

Citest uses an **isolated config** (subnet `172.31.0.0/24`, router `:18080`, gateway
`:18081`, mock ports `19xxx`) so tests can run while a dev `make up` stack is active on
default ports.

Harness: `citest/harness/` — temp workdir, `gencompose`, `docker compose up --wait`,
HTTP/gRPC helpers, log dump on failure.

## How to run

**Prerequisites:** Docker, Go 1.24+, linux **devshardd** binary for containers.

```bash
cd devshard/testenv
make build-devshardd
make citest-stack          # S1–S9
make citest-s9             # validation lease race only
```

Or run a single scenario:

```bash
TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run TestS3_ParamsLongPoll -v -timeout 30m
```

| Variable / tag | Purpose |
|----------------|---------|
| `TESTENV_CITEST=1` | Opt-in gate (`harness.SkipUnlessEnv`) |
| `-tags=testenvci` | Build tag on `s*_*.go` tests |
| `make citest-stack` | Builds mock images + runs all S1–S8 |

Wrapper script: [`scripts/run-stack-citest.sh`](../scripts/run-stack-citest.sh).

CI: `workflow_dispatch` with `integration: true`, or PR comment `/run-testenv` (OWNER/MEMBER) → `make -C devshard ci-testenv-integration`.

## Scenario index

| ID | Name | What we validate | Test |
|----|------|------------------|------|
| **S1** | Stack smoke | Full stack boots; all boundaries healthy | `TestS1_StackSmoke` |
| **S2** | Router stickiness | Same session → same versiond upstream | `TestS2_RouterStickiness` |
| **S3** | Params long-poll | Governance patch wakes `GetRuntimeConfig` | `TestS3_ParamsLongPoll` |
| **S4** | Epoch switch | Epoch advance fast-forwards chain + bumps epoch in long-poll | `TestS4_EpochSwitch` |
| **S5** | Gateway chat | devshardctl → router → devshardd → mock-openai (stream + non-stream) | `TestS5_GatewayChat` |
| **S6** | versiond fault & restart | Stop without failover; restart with session persistence | `TestS6_VersiondStop`, `TestS6_VersiondRestartPersistence` |
| **S7** | Legacy version pin | Non-HA path → `VERSIOND_LEGACY_HOST` only; HA path still multi-upstream | `TestS7_LegacyVersionPinnedToSingleHost` |
| **S8** | SQLite → HA-fail → PG migrate | §3.3 Phases 0–4: sqlite single-host, multi-host 503, postgres migrate, HA OK | `TestS8_SqliteHaFailMigrate` |
| **S9** | Validation lease race | 3-host HA pair + solo executor; 100% `validation_rate`; Postgres lease exclusivity PASS/FAIL; §7a/§7b | `TestS9_ValidationLeaseRaceCore`, `…PendingStretch`, `…StaleReclaim` |

Source: `devshard/testenv/citest/s{1..6}_*.go` (S6 spans `s6_versiond_stop_test.go` and `s6_versiond_restart_test.go`).

### Phase 12 transport scenarios (gRPC-only gateway)

Full plan: [`chain-transport-consolidation.md`](./chain-transport-consolidation.md).

| ID | Name | What we validate | Test | Status |
|----|------|------------------|------|--------|
| **G1** | gRPC escrow create | devshardctl creates escrow via `common/chain/tx` + mock-chain gRPC; escrow visible on gRPC `DevshardEscrow` query | `TestG1_GatewayEscrowCreateGRPC` | ✅ |
| **G2** | gRPC escrow read | Gateway reads escrow fields via gRPC bridge (no `RESTBridge` / LCD) | `TestG2_GatewayEscrowReadGRPC` | ✅ |
| **G3** | Chat without LCD | Same as S5 but compose omits `DEVSHARD_CHAIN_REST` and `DEVSHARD_TX_QUERY_REST` for gateway | `TestG3_GatewayChatGRPCOnly` | ✅ |
| **G4** | REST removed gate | Static test: no `NewRESTBridge` / `RESTChainTxClient` in devshardctl | `TestG4_NoRESTChainClientsInGatewayProduction` | ✅ |

Run: `make citest-grpc-transport` from `devshard/testenv/`.

---

## S1 — Stack smoke

**What we test:** The generated multi-versiond compose comes up cleanly and every
service boundary responds.

**How:**

1. `harness.BootS1Stack` — writes 2-host citest config, runs `gencompose`, `docker compose up --wait`.
2. `harness.WaitS1Healthy` — polls:
   - mock-chain RPC `/health`
   - mock-dapi `/healthz` and `/v1/epochs/latest`
   - versiond-router `/healthz`
   - devshardctl `/v1/status`
3. `stack.RequireServicesRunning` — `docker compose ps` shows `mock-chain`, `mock-dapi`,
   `mock-openai`, `versiond-router`, `devshardctl`, `devshard-postgres`, `versiond-0`,
   `versiond-1` running.

**Pass criteria:** All health endpoints return 2xx; all listed containers running. Implies
devshardd children started under versiond (protocol `v2`, chain dial to mock-chain).

---

## S2 — Router stickiness

**What we test:** versiond-router **consistent-hash** pins a session id to one upstream
versiond across repeated requests, and at least two distinct upstreams are reachable.

**How:**

1. Boot S1 stack; wait for router `/healthz`.
2. Hit `/{version}/sessions/{sessionA}/healthz` **8 times**; read `X-Upstream-Addr` header
   (exposed by router nginx template).
3. Assert every retry returns the **same** upstream address.
4. Probe up to 64 other session ids until one lands on a **different** upstream.

**Pass criteria:** Stable upstream for session A; at least one session B routes elsewhere.
Validates deploy/join-style sticky routing before chat or long-poll scenarios depend on it.

---

## S7 — Legacy version pinned to single host

**What we test:** versiond-router sends version prefixes listed in
`VERSIOND_NON_HA_VERSIONS` only to `VERSIOND_LEGACY_HOST` (`versiond_legacy`),
while other versions sticky-hash across `VERSIOND_HOSTS` (and get
`Devshard-Ha: true` for multi-host HA).

**How:**

1. Boot S1 stack (`VERSIOND_NON_HA_VERSIONS=v1`; legacy host `versiond-0`).
2. Probe `/v1/sessions/<id>/healthz` for 16 distinct session ids. Assert every
   response has `X-Versiond-Backend: versiond_legacy` and the same
   `X-Upstream-Addr` mapped to `versiond-0`.
3. Reuse S2-style probes on `VersionName` (e.g. `v2`); require ≥2 distinct
   upstreams and `X-Versiond-Backend: versiond_ha_pool`.
4. Stop the non-legacy versiond; repeat legacy probes — still pinned to
   `versiond-0`.

**Pass criteria:** Non-HA path never fans out; HA path still multi-upstream.
See `devshard/docs/pr-1366-deploy-test-plan.md` §3.2.

---

## S8 — SQLite → Devshard-Ha fail → Postgres migrate → HA

**What we test:** full §3.3 walkthrough from
`devshard/docs/pr-1366-deploy-test-plan.md` (Phases 0–4).

**How:**

1. Boot 2×versiond + Postgres compose patched to `DEVSHARD_STORAGE_MODE=sqlite`
   and `VERSIOND_HOSTS=versiond-0` only; stop `versiond-1`.
2. **Phase 0:** NON_HA `v1` → `versiond_legacy`; HA `VersionName` →
   `versiond_ha_pool` without multi-host `Devshard-Ha` (healthz 200 on sqlite).
3. **Phase 1:** Gateway chat ×3; inventory `{data}/versiond-0/<version>/_meta.db`
   (`escrow_epoch`).
4. **Phase 2:** Expand `VERSIOND_HOSTS` to both hosts; recreate router; start
   `versiond-1`. HA `/<version>/healthz` → **503**; gateway chat fails; NON_HA
   still legacy-pinned.
5. **Phase 3:** Patch `DEVSHARD_STORAGE_MODE=postgres`; recreate both versiond.
   Assert `*.migrated.*`, `.pg-bound`, and Postgres `devshard_session_index`
   matches the SQLite inventory.
6. **Phase 4:** Gateway chat OK; sticky fan-out across hosts; NON_HA unchanged.

**Pass criteria:** Multi-host + sqlite is rejected; migrate preserves escrow
index; HA + postgres serves. Test: `TestS8_SqliteHaFailMigrate`.

---

## S9 — Validation lease race (HA exclusivity)

**What we test:** join-style same-`KEY_NAME` HA replicas under
`validation_rate=10000` do not double-validate. Postgres
`devshard_validation_leases` uniqueness is the PASS/FAIL signal. Also covers
pending stretch (slow ML) and stale reclaim (short TTL + pause ML + stop
replica). Manual companion:
[`../../docs/validation-lease-race-manual-test.md`](../../docs/validation-lease-race-manual-test.md).

**Topology:** 3 versionds — `versiond-0`/`versiond-1` HA pair (same `KEY_NAME`),
`versiond-2` solo executor. Escrow slots alternate HA + solo so the HA
participant validates someone else's finished inferences (own executions are
never validated).

**How:**

1. Boot S9 stack (`WriteS9Config` / `BootS9Stack`); seed chat; warm escrow on
   all versionds.
2. **Core:** parallel lease monitor + chat load; require zero duplicate groups
   and ≥5 lease rows (`TestS9_ValidationLeaseRaceCore`).
3. **7a:** slow mock-openai; observe `pending ≥ 1`; restore ML; uniqueness PASS
   (`TestS9_ValidationLeaseRacePendingStretch`).
4. **7b:** short `DEVSHARD_VALIDATION_LEASE_TTL`; slow then **pause ML (503)**;
   stop one HA replica; wait TTL; restore ML; submitted grows; uniqueness PASS
   (`TestS9_ValidationLeaseRaceStaleReclaim`).

**Manual scripts:** `scripts/lease-race-run.sh` (monitor + load + PASS/FAIL).

**Pass criteria:** Monitor / citest report PASS (no duplicate lease keys);
optional paths prove pending visibility and stale reclaim after ML pause.

---

## S3 — Params long-poll

**What we test:** Lane-C governance fields flow **mock-chain → mock-dapi →
GetRuntimeConfig long-poll** the way production devshardd consumes `NODE_MANAGER_ADDR`.

**How:**

1. Boot S1 stack; dial mock-dapi NodeManager gRPC.
2. Read baseline `GetRuntimeConfig` (`max_nonce`, `refusal_timeout`, `params_block_height`).
3. Start a **blocked long-poll** at the baseline height (`max_wait` ≈ 25s).
4. `POST /testenv/params` on mock-dapi (proxied to mock-chain) with patched
   `max_nonce`, `refusal_timeout`, `execution_timeout`.
5. Assert long-poll **wakes** with higher `params_block_height` and patched values.
6. Assert caught-up client gets `unchanged=true`; stale-height client still receives full snapshot.

**Pass criteria:** Long-poll unblocks within 20s after param patch; snapshot fields match
patch. Exercises `common/runtimeconfig` server + client path without production dapi.

---

## S4 — Epoch switch

**What we test:** Epoch transition on mock-chain (block fast-forward to `next_poc_start`,
roll `next_poc_start` forward) propagates into **GetRuntimeConfig** (`current_epoch_id` bump).

**How:**

1. Boot S1 stack; read mock-chain epoch snapshot (`epoch_index`, `next_poc_start`, block height).
2. Baseline `GetRuntimeConfig` — record `current_epoch_id` and `params_block_height`.
3. Blocked long-poll at baseline height.
4. `POST /testenv/epoch` `{advance: true}` on mock-dapi.
5. Assert long-poll wakes with **higher** `current_epoch_id` and `params_block_height`.
6. Re-read mock-chain snapshot — block height ≥ previous `next_poc_start`, `poc_start` moved,
   `next_poc_start` += `epoch_length`.

**Pass criteria:** Epoch index increments; chain block cursor catches up; long-poll clients
see new epoch. Covers CometBFT RPC face (3b) + params notification path used at epoch change.

---

## S5 — Gateway chat

**What we test:** Full **MVP+ chat path**: devshardctl creates/uses escrow, routes through
sticky versiond-router to devshardd, which calls mock-openai — **non-stream and SSE stream**.

**How:**

1. Boot S1 stack; wait gateway `/v1/status` and devshardd health via router
   `/{version}/healthz`.
2. `POST /v1/chat/completions` on devshardctl (pooled chat, `stream=false`) with test API key.
3. Assert HTTP 200 and mock-openai deterministic assistant content/role.
4. Repeat with `stream=true`; assemble SSE chunks; assert same mock-openai content shape.

On failure, dumps logs for `devshardctl`, `versiond-0`, `versiond-1`, `mock-openai`.

**Pass criteria:** Both stream modes return 200 with expected mock-openai payload. Escrow
create/settle uses mock-chain gRPC only (see **G3** in
[`chain-transport-consolidation.md`](./chain-transport-consolidation.md)).

---

## S6 — versiond fault & restart

S6 covers two production-shaped versiond lifecycle paths: **upstream stop** (router fault
semantics) and **stop/start restart** (postgres-backed devshardd recovery with the same
gateway session).

### S6.1 — versiond stop (fault)

**What we test:** Behaviour when a **sticky upstream versiond is stopped** — nginx does not
silently proxy to a dead peer; sessions on a live upstream keep working.

**Test:** `TestS6_VersiondStop` (`citest/s6_versiond_stop_test.go`)

**How:**

1. Boot S1 stack; `harness.FindDistinctStickySessions` — two session ids on **different**
   upstreams (`X-Upstream-Addr`).
2. `docker compose stop` the host mapped to session A's upstream.
3. Retry session A's router URL — expect **502/503** (peer down) **or** consistent-hash
   reroute to surviving upstream (ring shrink); not success via dead peer.
4. Session B (live upstream) must still return 200 with correct `X-Upstream-Addr`.

**Pass criteria:** Fault outcome classified (`FaultRouteFailed` or `FaultRouteRerouted`);
surviving session keeps working. Documents **no transparent failover** for pinned sessions
(matching production router semantics).

### S6.2 — versiond restart persistence

**What we test:** The **versiond → devshardd → router → gateway** stack survives versiond
restarts without losing the active escrow session or regressing nonce/state. `devshardctl`
stays up; restarted devshardd children recover from Postgres.

**Test:** `TestS6_VersiondRestartPersistence` (`citest/s6_versiond_restart_test.go`)

**How:**

1. Boot S1 stack; wait gateway chat readiness and snapshot session via `/v1/status` +
   `/v1/debug/state` (`harness.GetGatewaySessionSnapshot`).
2. Gateway chat #1 — assert session nonce advances (`RequireGatewaySessionAdvanced`).
3. `docker compose stop` + `start` **one** versiond host (`harness.RestartService`);
   wait router + session `healthz` (`WaitVersiondSessionHealthy`).
4. Assert session stable across restart — same escrow, nonce, balance, phase
   (`RequireGatewaySessionStable`).
5. Gateway chat #2 — assert nonce advances again.
6. Restart **all** versiond hosts; wait healthy; assert stable again.
7. Gateway chat #3 — final nonce advance.

**Pass criteria:** Gateway chat succeeds after each restart; session nonce never regresses;
balance/phase unchanged immediately after restart (before the next chat). Validates
persistence across the multi-host topology, not only mock-chain or gateway in-memory state.

---

## Related tests (not S1–S8)

| Suite | Command | Scenarios |
|-------|---------|-----------|
| gRPC transport | `make citest-grpc-transport` | G1–G4 ✅ ([`chain-transport-consolidation.md`](./chain-transport-consolidation.md)) |
| Adversarial | `make citest-adversarial` | A1–A4 (fault injection on mock-openai / mock-chain) |
| Observability | `make citest-observability` | O1 Jaeger + Loki + Prometheus smoke |
| Gateway smoke | `TESTENV_GATEWAY_SMOKE=1` | Phase 7 wiring without full citest tag |

See [`README.md`](../README.md) for adversarial and observability detail.

## Not yet implemented

| ID | Scenario | Notes |
|----|----------|-------|
| **S7** | Same-name sha swap | Phase 13 — rolling update |
| **S8** | Router host drain | Phase 13 — upstream evacuation |

Tracked in [`testenv-v2-plan.md`](./testenv-v2-plan.md) § Phase 13.

---

## G1 — gRPC escrow create ✅

**What we test:** `common/chain/tx` creates a devshard escrow via mock-chain gRPC
(`BroadcastTx` + `GetTx` + auth `Account` query) — no LCD for the tx path.

**How:** `TestG1_GatewayEscrowCreateGRPC` boots S1 stack, dials mock-chain gRPC,
calls `chaintx.CreateDevshardEscrow`, queries `DevshardEscrow` on gRPC.

**Run:** `make citest-grpc-transport` (or `-run TestG1_`).

---

## G2 — gRPC escrow read ✅

**What we test:** Escrow read via `bridge.GRPCBridge` / `common/chain.Client` against dockerized mock-chain (no LCD).

**Test:** `TestG2_GatewayEscrowReadGRPC` — boots mock-chain only, reads escrow `1` via gRPC.

---

## G3 — Gateway chat without LCD ✅

**What we test:** S5-equivalent chat (non-stream + SSE) with gRPC-only gateway chain transport.

**How:** `TestG3_GatewayChatGRPCOnly` — full S1 stack with `docker compose up --build`; compose gate asserts no `DEVSHARD_CHAIN_REST` / `DEVSHARD_TX_QUERY_REST` on devshardctl.

**Pass criteria:** Non-stream + stream chat return 200.

**Run:** `make citest-grpc-transport` (or `-run TestG3_`).

---

## G4 — REST removed gate ✅

**What we test:** Production gateway code must not call REST chain clients.

**How:** `TestG4_NoRESTChainClientsInGatewayProduction` scans non-test `.go` files in `devshard/cmd/devshardctl` (excluding legacy `chain_tx_rest*.go`).

**Pass criteria:** Test fails if `NewRESTBridge` or `NewRESTChainTxClient` appear in production paths.
