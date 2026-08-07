# Stack citest scenarios

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

Citest uses an **isolated config** (subnet `172.31.0.0/24`) and lets Docker assign
localhost host ports for router, gateway, and mock services. The harness discovers the
actual ports with `docker compose port`, so tests can run while a dev `make up` stack is
active on default ports.

Harness: `citest/harness/` — temp workdir, `gencompose`, `docker compose up --wait`,
HTTP/gRPC helpers, log dump on failure.

## How to run

**Prerequisites:** Docker, Go 1.24+, linux **devshardd** binary for containers.

```bash
cd devshard/testenv
make build-devshardd
make citest-stack                 # all core stack behavior tests
make citest-validation-lease-race # validation lease race only
make citest-versiond-rolling-update
make citest-escrow-longpoll       # escrow long-poll warm (rebuilds devshardd)
make citest-ml-nodes              # multi mock-openai pool + per-node fault
```

Or run a single scenario:

```bash
TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run TestParamsLongPoll -v -timeout 30m
```

| Variable / tag | Purpose |
|----------------|---------|
| `TESTENV_CITEST=1` | Opt-in gate (`harness.SkipUnlessEnv`) |
| `-tags=testenvci` | Build tag on full-stack citests |
| `make citest-stack` | Builds mock images and runs all core stack behavior tests |

Wrapper script: [`scripts/run-stack-citest.sh`](../scripts/run-stack-citest.sh).

CI: `workflow_dispatch` with `integration: true`, or PR comment `/run-testenv`
(OWNER/MEMBER). The `devshard testenv` workflow discovers the runnable suites via
`make -C devshard/testenv list-citest-targets` and fans them out into a GitHub
Actions matrix — **one runner per suite, in parallel**. New `citest-*` suites are
picked up automatically (no workflow edit). For a local sequential subset, use
`make -C devshard ci-testenv-integration`.

## Scenario index

| Scenario | What we validate | Test |
|----------|------------------|------|
| **Stack smoke** | Full stack boots; all boundaries healthy | `TestStackSmoke` |
| **Router stickiness** | Same session → same versiond upstream | `TestRouterStickiness` |
| **Params long-poll** | Governance patch wakes `GetRuntimeConfig` | `TestParamsLongPoll` |
| **Epoch switch** | Epoch advance fast-forwards chain + bumps epoch in long-poll | `TestEpochSwitch` |
| **Gateway chat** | devshardctl → router → devshardd → mock-openai (stream + non-stream) | `TestGatewayChat` |
| **Versiond failover** | Stop → first-502 failover to survivor | `TestVersiondStickySessionFailover` |
| **Versiond restart persistence** | Restart preserves the gateway session | `TestVersiondRestartSessionPersistence` |
| **HA stale standby catch-up** | Sticky primary advances PG, stop it; stale standby catch-up without `23505` | `TestHAStaleStandbyCatchupIdempotent` |
| **Legacy version pin** | Non-HA path → `VERSIOND_LEGACY_HOST`; HA path remains multi-upstream | `TestLegacyVersionPinnedToSingleHost` |
| **SQLite to Postgres HA migration** | SQLite single-host, multi-host rejection, migration, HA recovery | `TestSQLiteToPostgresHAMigration` |
| **Validation lease race** | Same-key HA lease exclusivity, pending stretch, stale reclaim | `TestValidationLeaseRaceCore`, `…PendingStretch`, `…StaleReclaim` |
| **Versiond rolling update** | Postgres blue/green drain and hybrid fallback | `TestVersiondRollingUpdateSameVersionSHA`, `…HybridFallback` |
| **Escrow long-poll warm** | DAPI escrow-created host event → devshardd `escrow_cache` prefetch → first inference binds from cache with the live escrow query faulted | `TestEscrowLongPollWarmWithoutInferenceNode` |
| **ML node pool** | N mock-openai instances behind mock-dapi `AcquireMLNode`; a fault on one node leaves the others healthy | `TestMLNodePool_PerNodeFault` |

Source files under `devshard/testenv/citest/` use the same behavior-oriented
names. Versiond failover and restart persistence intentionally remain separate
test files because they exercise different lifecycle contracts.

### Phase 12 transport scenarios (gRPC-only gateway)

Full plan: [`chain-transport-consolidation.md`](./chain-transport-consolidation.md).

| ID | Name | What we validate | Test | Status |
|----|------|------------------|------|--------|
| **G1** | gRPC escrow create | devshardctl creates escrow via `common/chain/tx` + mock-chain gRPC; escrow visible on gRPC `DevshardEscrow` query | `TestGatewayEscrowCreateGRPC` | ✅ |
| **G2** | gRPC escrow read | Gateway reads escrow fields via gRPC bridge (no `RESTBridge` / LCD) | `TestGatewayEscrowReadGRPC` | ✅ |
| **G3** | Chat without LCD | Gateway chat with compose omitting `DEVSHARD_CHAIN_REST` and `DEVSHARD_TX_QUERY_REST` | `TestGatewayChatGRPCOnly` | ✅ |
| **G4** | REST removed gate | Static test: no `NewRESTBridge` / `RESTChainTxClient` in devshardctl | `TestNoRESTChainClientsInGatewayProduction` | ✅ |

Run: `make citest-grpc-transport` from `devshard/testenv/`.

---

## Stack smoke

**What we test:** The generated multi-versiond compose comes up cleanly and every
service boundary responds.

**How:**

1. `harness.BootStack` — writes 2-host citest config, runs `gencompose`, `docker compose up --wait`.
2. `harness.WaitStackHealthy` — polls:
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

## Router stickiness

**What we test:** versiond-router **consistent-hash** pins a session id to one upstream
versiond across repeated requests, and at least two distinct upstreams are reachable.

**How:**

1. Boot the standard stack; wait for router `/healthz`.
2. Hit `/{version}/sessions/{sessionA}/healthz` **8 times**; read `X-Upstream-Addr` header
   (exposed by router nginx template).
3. Assert every retry returns the **same** upstream address.
4. Probe up to 64 other session ids until one lands on a **different** upstream.

**Pass criteria:** Stable upstream for session A; at least one session B routes elsewhere.
Validates deploy/join-style sticky routing before chat or long-poll scenarios depend on it.

---

## Legacy version pinned to single host

**What we test:** versiond-router sends version prefixes listed in
`VERSIOND_NON_HA_VERSIONS` only to `VERSIOND_LEGACY_HOST` (`versiond_legacy`),
while other versions sticky-hash across `VERSIOND_HOSTS` (and get
`Devshard-Ha: true` for multi-host HA).

**How:**

1. Boot the standard stack (`VERSIOND_NON_HA_VERSIONS=v1`; legacy host
   `versiond-0`).
2. Probe `/v1/sessions/<id>/healthz` for 16 distinct session ids. Assert every
   response has `X-Versiond-Backend: versiond_legacy` and the same
   `X-Upstream-Addr` mapped to `versiond-0`.
3. Reuse router-stickiness probes on `VersionName` (e.g. `v2`); require ≥2 distinct
   upstreams and `X-Versiond-Backend: versiond_ha_pool`.
4. Stop the non-legacy versiond; repeat legacy probes — still pinned to
   `versiond-0`.

**Pass criteria:** Non-HA path never fans out; HA path still multi-upstream.
See `devshard/docs/pr-1366-deploy-test-plan.md` §3.2.

---

## SQLite → Devshard-Ha fail → Postgres migrate → HA

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
index; HA + postgres serves. Test: `TestSQLiteToPostgresHAMigration`.

---

## Versiond rolling update

**What we test:** A governance-style `/versions` change that keeps the same
version name but changes archive `sha256` causes `versiond` to download the new
devshardd archive. In Postgres mode, `versiond` runs a blue/green swap and
drains the old child without dropping already accepted work. In hybrid mode, the
same change falls back to stop-then-start and must not overlap old and new
children.

**How:**

1. Boot the standard stack with `VERSIOND_OVERRIDE_<version>` removed, so `versiond`
   downloads devshardd from mock-dapi rather than copying a local override.
2. Serve two zip archives from mock-dapi `/testenv/binaries/*`; both contain the
   real linux `devshardd`, but have different archive sha values.
3. Wait both versiond hosts to report old sha `running` in `/healthz`.
4. For each versiond host, boot a fresh stack with versiond-router pinned to
   that host, slow mock-openai SSE chunks, start a streaming gateway chat, and
   wait for the first content chunk from the old child.
5. `POST /testenv/versions` with the same version name and new archive sha.
6. Poll the pinned versiond host `/healthz`; require a moment where a new child
   is `running` while the old sha is `draining`. The positive test repeats this
   for both versiond hosts.
7. Send a new gateway chat and require success while the old stream is still
   finishing; continue probing router health during the swap.
8. Require the original stream to finish with `[DONE]` and the old draining child
   to disappear.

**Pass criteria:** In Postgres mode, no router-health interruption occurs during
the swap; new traffic succeeds on the new child; the old stream completes; each
versiond host is exercised in a pinned subtest and shows the expected
`running(new sha)` + `draining(old sha)` overlap. In hybrid mode, both hosts
converge to the new sha without ever reporting an old draining child.

Tests: `TestVersiondRollingUpdateSameVersionSHA` and
`TestVersiondRollingUpdateHybridFallback`.

---

## Validation lease race (HA exclusivity)

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

1. Boot the validation lease race stack (`WriteValidationLeaseRaceConfig` /
   `BootValidationLeaseRaceStack`); seed chat; warm escrow on all versionds.
2. **Core:** parallel lease monitor + chat load; require zero duplicate groups
   and ≥5 lease rows (`TestValidationLeaseRaceCore`).
3. **7a:** slow mock-openai; observe `pending ≥ 1`; restore ML; uniqueness PASS
   (`TestValidationLeaseRacePendingStretch`).
4. **7b:** short `DEVSHARD_VALIDATION_LEASE_TTL`; slow then **pause ML (503)**;
   stop one HA replica; wait TTL; restore ML; submitted grows; uniqueness PASS
   (`TestValidationLeaseRaceStaleReclaim`).

**Manual scripts:** `scripts/lease-race-run.sh` (monitor + load + PASS/FAIL).

**Pass criteria:** Monitor / citest report PASS (no duplicate lease keys);
optional paths prove pending visibility and stale reclaim after ML pause.

---

## Params long-poll

**What we test:** Lane-C governance fields flow **mock-chain → mock-dapi →
GetRuntimeConfig long-poll** the way production devshardd consumes `NODE_MANAGER_ADDR`.

**How:**

1. Boot the standard stack; dial mock-dapi NodeManager gRPC.
2. Read baseline `GetRuntimeConfig` (`max_nonce`, `refusal_timeout`, `params_block_height`).
3. Start a **blocked long-poll** at the baseline height (`max_wait` ≈ 25s).
4. `POST /testenv/params` on mock-dapi (proxied to mock-chain) with patched
   `max_nonce`, `refusal_timeout`, `execution_timeout`.
5. Assert long-poll **wakes** with higher `params_block_height` and patched values.
6. Assert caught-up client gets `unchanged=true`; stale-height client still receives full snapshot.

**Pass criteria:** Long-poll unblocks within 20s after param patch; snapshot fields match
patch. Exercises `common/runtimeconfig` server + client path without production dapi.

---

## Epoch switch

**What we test:** Epoch transition on mock-chain (block fast-forward to `next_poc_start`,
roll `next_poc_start` forward) propagates into **GetRuntimeConfig** (`current_epoch_id` bump).

**How:**

1. Boot the standard stack; read mock-chain epoch snapshot (`epoch_index`,
   `next_poc_start`, block height).
2. Baseline `GetRuntimeConfig` — record `current_epoch_id` and `params_block_height`.
3. Blocked long-poll at baseline height.
4. `POST /testenv/epoch` `{advance: true}` on mock-dapi.
5. Assert long-poll wakes with **higher** `current_epoch_id` and `params_block_height`.
6. Re-read mock-chain snapshot — block height ≥ previous `next_poc_start`, `poc_start` moved,
   `next_poc_start` += `epoch_length`.

**Pass criteria:** Epoch index increments; chain block cursor catches up; long-poll clients
see new epoch. Covers CometBFT RPC face (3b) + params notification path used at epoch change.

---

## Gateway chat

**What we test:** Full **MVP+ chat path**: devshardctl creates/uses escrow, routes through
sticky versiond-router to devshardd, which calls mock-openai — **non-stream and SSE stream**.

**How:**

1. Boot the standard stack; wait gateway `/v1/status` and devshardd health via router
   `/{version}/healthz`.
2. `POST /v1/chat/completions` on devshardctl (pooled chat, `stream=false`) with test API key.
3. Assert HTTP 200 and mock-openai deterministic assistant content/role.
4. Repeat with `stream=true`; assemble SSE chunks; assert same mock-openai content shape.

On failure, dumps logs for `devshardctl`, `versiond-0`, `versiond-1`, `mock-openai`.

**Pass criteria:** Both stream modes return 200 with expected mock-openai payload. Escrow
create/settle uses mock-chain gRPC only (see **G3** in
[`chain-transport-consolidation.md`](./chain-transport-consolidation.md)).

---

## Versiond fault and restart

These tests cover two production-shaped versiond lifecycle paths: **upstream
stop** (router failover) and **stop/start restart** (Postgres-backed devshardd
recovery with the same gateway session).

### Versiond sticky-session failover

**What we test:** Behaviour when a **sticky upstream versiond is stopped** — nginx
reroutes on the first upstream **502** / connect failure (`proxy_next_upstream`) to a
surviving peer; sessions already hashed to a live upstream keep working.

**Test:** `TestVersiondStickySessionFailover` (`citest/versiond_failover_test.go`)

**How:**

1. Boot the standard stack; `harness.FindDistinctStickySessions` — two session ids on **different**
   upstreams (`X-Upstream-Addr`).
2. `docker compose stop` the host mapped to session A's upstream.
3. Retry session A's router URL — expect **non-gateway** response with survivor in
   `X-Upstream-Addr` (first-502 failover; not sticky-502 forever).
4. Session B (live upstream) must still succeed with correct `X-Upstream-Addr`.

**Pass criteria:** Sticky session on the stopped host fails over to the survivor;
surviving session keeps working. Mid-stream SSE after StartConfirm is **not** spliced
(client reconnects with a new request) — out of scope for this healthz probe.

### Versiond restart persistence

**What we test:** The **versiond → devshardd → router → gateway** stack survives versiond
restarts without losing the active escrow session or regressing nonce/state. `devshardctl`
stays up; restarted devshardd children recover from Postgres.

**Test:** `TestVersiondRestartSessionPersistence` (`citest/versiond_restart_persistence_test.go`)

**How:**

1. Boot the standard stack; wait gateway chat readiness and snapshot session via `/v1/status` +
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

## Related test suites

| Suite | Command | Scenarios |
|-------|---------|-----------|
| gRPC transport | `make citest-grpc-transport` | G1–G4 ✅ ([`chain-transport-consolidation.md`](./chain-transport-consolidation.md)) |
| Adversarial | `make citest-adversarial` | A1–A4 (fault injection on mock-openai / mock-chain) |
| Observability | `make citest-observability` | O1 smoke, C1/C2 trace↔log correlation, C3/C4 disposition, C5a payload capture, C7 jaeger-promtail regression, C8/C9 mock-dapi hop + shadow multi-host — status table in [`observability-test-plan.md`](./observability-test-plan.md) §0 |
| Observability (T5 only) | `make citest-dapi-correlation` | C8/C9 subset of the above, for iterating on the node-selection hop |
| ML node pool | `make citest-ml-nodes` | T7 per-node ML fault targeting (below) |
| Gateway smoke | `TESTENV_GATEWAY_SMOKE=1` | Phase 7 wiring without full citest tag |

Observability scenario ids (C1–C9, S1–S17, the I invariants and the F fault taxonomy) are **not**
duplicated here — [`observability-test-plan.md`](./observability-test-plan.md) §0 is the single
status page for what is landed, skipped or planned. Design rationale lives in
[`observability-trace-correlation-plan.md`](./observability-trace-correlation-plan.md), and the
mock-dapi hop and shadow multi-host cases (C8/C9) in
[`observability-t5-test-plan.md`](./observability-t5-test-plan.md).

See [`README.md`](../README.md) for adversarial and observability detail.

## G1 — gRPC escrow create ✅

**What we test:** `common/chain/tx` creates a devshard escrow via mock-chain gRPC
(`BroadcastTx` + `GetTx` + auth `Account` query) — no LCD for the tx path.

**How:** `TestGatewayEscrowCreateGRPC` boots the standard stack, dials mock-chain gRPC,
calls `chaintx.CreateDevshardEscrow`, queries `DevshardEscrow` on gRPC.

**Run:** `make citest-grpc-transport` (or `-run TestGatewayEscrowCreateGRPC`).

---

## G2 — gRPC escrow read ✅

**What we test:** Escrow read via `bridge.GRPCBridge` / `common/chain.Client` against dockerized mock-chain (no LCD).

**Test:** `TestGatewayEscrowReadGRPC` — boots mock-chain only, reads escrow `1` via gRPC.

---

## G3 — Gateway chat without LCD ✅

**What we test:** Gateway chat (non-stream + SSE) with gRPC-only chain transport.

**How:** `TestGatewayChatGRPCOnly` — full standard stack with
`docker compose up --build`; the compose gate asserts that devshardctl has no
`DEVSHARD_CHAIN_REST` or `DEVSHARD_TX_QUERY_REST`.

**Pass criteria:** Non-stream + stream chat return 200.

**Run:** `make citest-grpc-transport` (or `-run TestGatewayChatGRPCOnly`).

---

## G4 — REST removed gate ✅

**What we test:** Production gateway code must not call REST chain clients.

**How:** `TestNoRESTChainClientsInGatewayProduction` scans non-test `.go` files in `devshard/cmd/devshardctl`.

**Pass criteria:** Test fails if `NewRESTBridge` or `NewRESTChainTxClient` appear in production paths.

---

## Escrow long-poll warm (first inference without live escrow fetch)

**What we test:** the escrow long-poll warm path from
[`../../docs/v4-deploy-test-plan.md`](../../docs/v4-deploy-test-plan.md) §6.1–§6.3.
DAPI publishes an escrow-created event over NodeManager `GetHostEvents`; v4
`devshardd` consumes it and prefetches escrow metadata into `escrow_cache`
**before any chat**. A first inference for that escrow then binds from the warm
cache even when the live chain escrow query is unavailable — i.e. without a
request-time round-trip through the escrow fetch path.

**Production wiring exercised (PR #1443 consumer side):**

- `devshardd` starts `devshard/hostevents.Run` (gated by
  `DEVSHARD_HOST_EVENTS_ENABLED`, default on) against `NODE_MANAGER_ADDR`.
- The warm sink (`cmd/devshardd/hostevents_sink.go`) fetches escrow metadata
  from chain and writes `storage.PutEscrowCache`.
- Lazy bind reads through `CachingEscrowBridge`
  (`cmd/devshardd/bridge/caching.go`): chain-first, with `escrow_cache` fallback
  when the live query fails.

**Mock support:**

- mock-dapi implements `GetHostEvents` over an in-memory ring
  (`mockdapi/hostevents.go`); `POST /testenv/host-events/escrow-created`
  appends an event.
- mock-chain can fault the `DevshardEscrow` query
  (`POST /testenv/escrow-query-fault`) to simulate the request-time escrow
  fetch path being down.

**How:** `TestEscrowLongPollWarmWithoutInferenceNode`

1. Boot the standard stack; wait gateway + router health. Read the gateway
   escrow id.
2. Assert `devshard_escrow_cache` has **no** row for the escrow (no chat yet).
3. `POST /testenv/host-events/escrow-created` for the escrow; poll shared
   Postgres until the warm row appears (before any chat).
4. `POST /testenv/escrow-query-fault {faulted:true}` — live escrow fetch now
   fails.
5. Gateway chat for the escrow → **200** with mock-openai content; the first
   bind is served from the warm cache.

**Pass criteria:** warm row appears from the long-poll (not a chat); first
inference succeeds with the live escrow query faulted.

**Run:** `make citest-escrow-longpoll` (rebuilds `devshardd`; or `-run TestEscrowLongPollWarm`).

---

## ML node pool — per-node fault targeting (T7)

**What we test:** the stack can express "one ML node is broken while the others are fine".
Before T7 a single `mock-openai` served every host, so any fault hit all attempts at once and
winner/loser asymmetry was impossible to stage. `ml_nodes: N` in the citest config makes
`gencompose` emit `mock-openai-0…N-1`, and mock-dapi hands them out through real `AcquireMLNode`
node ids instead of a single hard-coded endpoint.

**How:** `TestMLNodePool_PerNodeFault`

1. `harness.BootMLNodePoolStack` with `ml_nodes: 2`; wait for stack health.
2. Dial mock-dapi NodeManager gRPC; two `AcquireMLNode` calls must return **distinct** `NodeId`s
   drawn from the pool, then release both `LockId`s.
3. `harness.PatchMockOpenAIFaultForNode` applies `latency_ms: 1500` to `mock-openai-1` only.
4. Chat directly against each node: node-0 stays under 800 ms, node-1 takes at least 1200 ms.
5. `harness.StopMLNode("mock-openai-1")` — that instance refuses connections while node-0 still
   serves `/healthz` 200; restart it at the end.

**Pass criteria:** distinct node ids per acquire, fault and stop confined to the targeted instance.
This closes gap **G1** of [`observability-test-plan.md`](./observability-test-plan.md) and is the
prerequisite for its S3 (`finished_unused` loser) and S16 (slow node vs. fast node) scenarios.

**Run:** `make citest-ml-nodes` (or `-run TestMLNodePool_`).

---

## Validation-failure payload capture (C5b) — planned

**What we will test:** when a validator returns `Valid: false` — including the hash-mismatch path —
a payload line exists for the invalidated `inference_id` carrying the full prompt and response, so
the disagreement between executor and validator can be reconstructed from the two bodies.

**Why it is not here yet.** The gateway only learns of invalidation through the accounting recorder
(`ProtocolInvalidated`), long after the request completed, and it cannot know in advance which
inferences will be sampled. Capturing at the gateway therefore means retaining payloads for **all**
traffic across the validation window — a full-traffic payload store that needs Postgres, not the
SQLite accounting store. This is T4b, blocked on `ak/gateway-v2-postgres`.

**Fallback if the Postgres branch slips:** capture inside `cmd/devshardd/inference/validator.go`,
which already holds both payloads at the decision point, before the hash-mismatch check discards
them. That costs no storage but joins on `inference_id` + payload hash rather than on `trace_id`,
because validation runs in a different process on a different trace.

Design: [`observability-trace-correlation-plan.md`](./observability-trace-correlation-plan.md) §6.5.
The landed ML-failure half of payload capture is C5a
([`observability-test-plan.md`](./observability-test-plan.md) §0).

---

## Chaining vs. parallelism

Every full-stack citest boots a Docker Compose stack that pins a **fixed subnet
`172.31.0.0/24`** (`citest/harness/config.go` → `cmd/gencompose/compose.go`).
Host ports are Docker-assigned (no clash), but two stacks with the same subnet
**cannot be `up` at once on one host** — the second network create fails. This
is why each Makefile group runs in a single `go test` process with a `-run`
pattern and **no `t.Parallel()`**.

**Must be chained (sequential — order/state dependent):**

- Steps *within* a scenario are ordered and stateful:
  - `TestSQLiteToPostgresHAMigration` — phases 0→1→2→3→4 (migration is
    irreversible mid-test).
  - `TestVersiondRollingUpdate*` — create → swap → drain, with per-host subtests
    run one at a time.
  - `TestValidationLeaseRace*` — seed → warm → load/monitor → verify.
  - **`TestEscrowLongPollWarmWithoutInferenceNode`** — baseline → host-event
    warm → assert cache row *before* chat → fault escrow query → first chat. The
    first inference must be the first bind (so it exercises the cache path), so
    nothing may chat that escrow earlier.
- **On a single host, all full-stack scenarios are effectively serial** because
  of the shared subnet. This is the current model.

**Can run in parallel (only across isolated hosts / CI runners, or if
`network.subnet` + `base_ip` are parameterized per stack):**

- Distinct top-level scenarios that each `BootStack` are logically independent
  (own project dir, own stack, Docker-assigned ports): `TestStackSmoke`,
  `TestRouterStickiness`, `TestGatewayChat`, `TestEpochSwitch`,
  `TestParamsLongPoll`, `TestLegacyVersionPinnedToSingleHost`, the `G1/G2/G3`,
  `A1–A4`, the observability suite (`O1`, correlation, disposition, `C5a`,
  jaeger-promtail regression), `TestMLNodePool_PerNodeFault`, and **escrow
  long-poll warm**.
- They are grouped into separate Makefile targets (`citest-stack`,
  `citest-validation-lease-race`, `citest-versiond-rolling-update`,
  `citest-adversarial`, `citest-grpc-transport`, `citest-observability`,
  `citest-ml-nodes`, `citest-escrow-longpoll`) so CI can fan them out to
  **separate runners** — the supported form of parallelism today. CI
  enumerates these targets automatically
  with `make list-citest-targets` (excludes the `citest-images` /
  `citest-stack-build` helpers) and builds a GitHub Actions matrix from the list,
  so adding a new `citest-*` suite runs it in parallel with no workflow change.
- **Escrow long-poll warm caveat:** although it can run on its own runner in
  parallel with other *groups*, it must **not** share a stack (or run in the same
  `-run` batch) with another scenario that chats its escrow, or the "first
  inference binds from cache" assertion is invalidated. Keep it in its own
  `citest-escrow-longpoll` target.

To parallelize on a single host, parameterize `network.subnet` / `base_ip` per
stack (e.g. derive from a worker index); it is hard-coded today.
