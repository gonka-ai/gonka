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
| **versiond-router** | Sticky HAProxy (consistent hash on session id, per-version health-checked pools) |
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
make citest-payload-withholding   # executor payload withholding (500 → invalidate)
make citest-versiond-rolling-update
make citest-escrow-longpoll       # escrow long-poll warm (rebuilds devshardd)
make citest-adversarial           # Phase 9 A1–A5 (A5 is 3-host)
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
| **Payload withholding** | Executor `GET /payloads` 500 → Challenged/Invalidated; D7 off releases the lease | `TestPayloadWithholding_AllCallers500_Invalidates`, `…SelectiveValidator_Challenges`, `…D7Off_LeaseReleasedAndReacquired` |
| **Versiond rolling update** | Postgres blue/green drain and hybrid fallback | `TestVersiondRollingUpdateSameVersionSHA`, `…HybridFallback` |
| **Escrow long-poll warm** | DAPI escrow-created host event → devshardd `escrow_cache` prefetch → first inference binds from cache with the live escrow query faulted | `TestEscrowLongPollWarmWithoutInferenceNode` |

Source files under `devshard/testenv/citest/` use the same behavior-oriented
names. Versiond failover and restart persistence intentionally remain separate
test files because they exercise different lifecycle contracts.

### Phase 9 adversarial scenarios

| ID | Name | What we validate | Test | Status |
|----|------|------------------|------|--------|
| **A1** | Lost first SSE chunk | `mock-openai` `drop_first_chunk` → gateway stream still completes; assembled text missing first rune | `TestA1_LostFirstChunk` | ✅ |
| **A2** | ML upstream 5xx | `mock-openai` `http_status=503` → gateway chat HTTP ≥400 | `TestA2_MLUpstream5xx` | ✅ |
| **A3** | Stale escrow | `POST /testenv/escrow` settle → mock-chain gRPC reports `settled=true` | `TestA3_StaleEscrow` | ✅ |
| **A4** | Bad warm-key | `POST /testenv/grantees` revoke → warm grantee absent from `GranteesByMessageType` | `TestA4_BadWarmKey` | ✅ |
| **A5** | Error-finish miss | HTTP 200 SSE error envelope → `MsgErrorMiss`: client `hostApplicationError`, executor `Missed++`, no validation job, full client refund, `HostStats.Cost` unchanged | `TestA5_ErrorFinishMiss` | ✅ |

A1–A4 boot the standard 2× versiond stack. A5 boots a **3-host** stack so two
non-executor verifiers can exceed `VoteThreshold` (step 10 of
[`error-finish-miss-protocol-plan.md`](../docs/error-finish-miss-protocol-plan.md)).

Run: `make citest-adversarial` from `devshard/testenv/`.

### Phase 12 transport scenarios (gRPC-only gateway)

Full plan: [`chain-transport-consolidation.md`](./chain-transport-consolidation.md).

| ID | Name | What we validate | Test | Status |
|----|------|------------------|------|--------|
| **G1** | gRPC escrow create | devshardctl creates escrow via `common/chain/tx` + mock-chain gRPC; escrow visible on gRPC `DevshardEscrow` query | `TestG1_GatewayEscrowCreateGRPC` | ✅ |
| **G2** | gRPC escrow read | Gateway reads escrow fields via gRPC bridge (no `RESTBridge` / LCD) | `TestG2_GatewayEscrowReadGRPC` | ✅ |
| **G3** | Chat without LCD | Gateway chat with compose omitting `DEVSHARD_CHAIN_REST` and `DEVSHARD_TX_QUERY_REST` | `TestG3_GatewayChatGRPCOnly` | ✅ |
| **G4** | REST removed gate | Static test: no `NewRESTBridge` / `RESTChainTxClient` in devshardctl | `TestG4_NoRESTChainClientsInGatewayProduction` | ✅ |

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
   (set by the router on every response).
3. Assert every retry returns the **same** upstream address.
4. Probe up to 64 other session ids until one lands on a **different** upstream.

**Pass criteria:** Stable upstream for session A; at least one session B routes elsewhere.
Validates deploy/join-style sticky routing before chat or long-poll scenarios depend on it.

---

## Legacy version pinned to single host

**What we test:** versiond-router sends version prefixes listed in
`VERSIOND_NON_HA_VERSIONS` only to `VERSIOND_LEGACY_HOST` (`versiond_legacy`),
while other versions sticky-hash across the `versiond-pool` members (and get
`Devshard-Ha: true` for multi-host HA).

**How:**

1. Boot the standard stack with an empty static version floor. Wait until the
   governance catalog admits `VersionName` into a `versiond_dynamic_*` pool and
   verify that the pool reaches both versiond hosts.
2. Recreate only the router with `VersionName` in
   `VERSIOND_NON_HA_VERSIONS` and `versiond-0` as the legacy host.
3. Probe `/<VersionName>/sessions/<id>/healthz` for 16 distinct session ids. Assert every
   response has `X-Versiond-Backend: versiond_legacy` and the same
   `X-Upstream-Addr` mapped to `versiond-0`.
4. Stop the non-legacy versiond; repeat legacy probes — still pinned to
   `versiond-0`.

**Pass criteria:** governance admission creates a working multi-host dynamic
pool, then the explicit non-HA pin constrains that same route to one host.
See `devshard/docs/pr-1366-deploy-test-plan.md` §3.2.

---

## Dynamic catalog removal and readmission

**What we test:** the router and the real versiond supervisors interpret a
non-empty governance snapshot as the same desired set.

**How:**

1. Start both versiond hosts from the catalog and wait for the dynamic route.
2. Replace the catalog with a non-empty set that omits the running version;
   require both children to retire and the old path to return `503`.
3. Stop one host and re-add the version; require the surviving child to start,
   while the router still returns `503` behind its two-host activation reserve.
4. Start the second host and require the dynamic route to become available.

**Pass criteria:** removal reaches both control loops, and re-addition cannot
reuse the old route without satisfying admission again.

---

## SQLite → Devshard-Ha fail → Postgres migrate → HA

**What we test:** full §3.3 walkthrough from
`devshard/docs/pr-1366-deploy-test-plan.md` (Phases 0–4).

**How:**

1. Boot 2×versiond + Postgres compose patched to `DEVSHARD_STORAGE_MODE=sqlite`
   and `GONKA_HA=""`; stop `versiond-1`, removing it from the DNS pool.
2. **Phase 0:** NON_HA `v1` → `versiond_legacy`; HA `VersionName` → its
   `versiond_pool_*` without multi-host `Devshard-Ha` (healthz 200 on sqlite).
3. **Phase 1:** Gateway chat ×3; inventory `{data}/versiond-0/<version>/_meta.db`
   (`escrow_epoch`).
4. **Phase 2:** Set `GONKA_HA=true`; recreate router; start
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

## Payload withholding (executor fetch failure)

**What we test:** a withholding executor that fails `GET /sessions/:id/payloads`
with HTTP 500 cannot suppress validation. Validators publish `MsgValidation{Valid:false}`
(`Reason: executor_payload_unavailable`), the inference reaches `Challenged`,
mandatory Phase B votes settle it `Invalidated`. A second scenario faults only
one validator address. A third turns D7 off so the failure stays an error:
the Postgres lease is **released** (no 30m pending park) and a later attempt
can acquire well inside the TTL.

**Topology:** 4 versionds — HA pair + two solos (3 distinct addresses) so
Phase B still has a voter after the challenger. `validation_rate=10000`.
Fault injection is `DEVSHARD_TESTENV_PAYLOAD_HTTP_STATUS` /
`DEVSHARD_TESTENV_PAYLOAD_FAULT_VALIDATOR` on versiond (not mock-openai).

The env vars are only read by a `devshardd` compiled with the `devshard_testenv`
build tag. `make build-devshardd` in `testenv/` sets it; release builds do not,
so a stray env var cannot make a production executor withhold payloads. Run this
suite via `make citest-payload-withholding`, which rebuilds the tagged binary — a
binary from a plain `make devshardd-build` silently ignores the fault and the
tests will time out waiting for a challenge.

**How:**

1. Boot `WritePayloadWithholdingConfig` / `BootPayloadWithholdingStack`.
2. **All callers 500:** every payload GET returns 500. Drive chat until
   `/v1/debug/inferences` shows `invalidated` (`TestPayloadWithholding_AllCallers500_Invalidates`).
3. **Selective:** 500 only for `hosts[2]`'s address. Assert `challenged`
   (`TestPayloadWithholding_SelectiveValidator_Challenges`).
4. **D7 off:** `DEVSHARD_VALIDATION_VOTE_FALSE_ON_FETCH_FAILURE=false`. Observe
   a pending lease, then pending=0, then a later acquire (`TestPayloadWithholding_D7Off_LeaseReleasedAndReacquired`).

**Pass criteria:** unfixed code keeps inferences `Finished` and parks the lease
for 30m; fixed code challenges/invalidates and frees the row.

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

**What we test:** Behaviour when a **sticky upstream versiond is stopped** — the
router redispatches a connection failure to a
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
| Adversarial | `make citest-adversarial` | A1–A5 (fault injection on mock-openai / mock-chain) |
| Observability | `make citest-observability` | O1 Jaeger + Loki + Prometheus smoke |
| Gateway smoke | `TESTENV_GATEWAY_SMOKE=1` | Phase 7 wiring without full citest tag |

See [`README.md`](../README.md) for adversarial and observability detail.

## G1 — gRPC escrow create ✅

**What we test:** `common/chain/tx` creates a devshard escrow via mock-chain gRPC
(`BroadcastTx` + `GetTx` + auth `Account` query) — no LCD for the tx path.

**How:** `TestG1_GatewayEscrowCreateGRPC` boots the standard stack, dials mock-chain gRPC,
calls `chaintx.CreateDevshardEscrow`, queries `DevshardEscrow` on gRPC.

**Run:** `make citest-grpc-transport` (or `-run TestG1_`).

---

## G2 — gRPC escrow read ✅

**What we test:** Escrow read via `bridge.GRPCBridge` / `common/chain.Client` against dockerized mock-chain (no LCD).

**Test:** `TestG2_GatewayEscrowReadGRPC` — boots mock-chain only, reads escrow `1` via gRPC.

---

## G3 — Gateway chat without LCD ✅

**What we test:** Gateway chat (non-stream + SSE) with gRPC-only chain transport.

**How:** `TestG3_GatewayChatGRPCOnly` — full standard stack with
`docker compose up --build`; the compose gate asserts that devshardctl has no
`DEVSHARD_CHAIN_REST` or `DEVSHARD_TX_QUERY_REST`.

**Pass criteria:** Non-stream + stream chat return 200.

**Run:** `make citest-grpc-transport` (or `-run TestG3_`).

---

## G4 — REST removed gate ✅

**What we test:** Production gateway code must not call REST chain clients.

**How:** `TestG4_NoRESTChainClientsInGatewayProduction` scans non-test `.go` files in `devshard/cmd/devshardctl`.

**Pass criteria:** Test fails if `NewRESTBridge` or `NewRESTChainTxClient` appear in production paths.

---

## A5 — Error-finish miss ✅

**What we test:** a streamed OpenAI error envelope (HTTP 200 SSE, companion to A2's
HTTP 503) is accounted as `MsgErrorMiss`. Accounting changes; the served
client response does not.

Specified as step 10 of
[`error-finish-miss-protocol-plan.md`](../docs/error-finish-miss-protocol-plan.md).

**How:** `TestA5_ErrorFinishMiss` boots a 3-host stack
(`BootErrorMissAdversarialStack`) so two non-executor verifiers can exceed
`VoteThreshold`. `POST /testenv/fault` sets mock-openai `stream_error_envelope`;
the test then posts a non-stream chat through the gateway.

**Pass criteria:**

- Client HTTP 500 `hostApplicationError` with today's EngineCore body.
- Inference reaches `StatusTimedOut`; executor slot `HostStats.Missed` increments
  by 1 (settlement copies this counter).
- Client balance unchanged (full `ReservedCost` refund).
- Executor `HostStats.Cost` unchanged.
- No validation votes (`VotesValid` / `VotesInvalid` stay 0) and
  `CompletedValidations` unchanged.

**Run:** `make citest-adversarial` (or `-run TestA5_`).

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
  `A1–A5`, `O1`, and **escrow long-poll warm**.
- They are grouped into separate Makefile targets (`citest-stack`,
  `citest-validation-lease-race`, `citest-payload-withholding`, `citest-versiond-rolling-update`,
  `citest-adversarial`, `citest-grpc-transport`, `citest-observability`,
  `citest-escrow-longpoll`) so CI can fan them out to **separate runners** — the
  supported form of parallelism today. CI enumerates these targets automatically
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
