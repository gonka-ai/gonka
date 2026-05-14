# Container-level E2E plan — height-sync anchor + future scenarios

This plan describes how to **re-implement every scenario currently covered by
in-process `httptest` tests** in `devshard/testenv/scenarios/` against the
**real Docker Compose stack** that `devshard/testenv` already ships. It
locks in the patterns we want every container-level scenario to follow so
new tests do not each re-invent harness code.

Source documents:

- `[devshard/plans/height-sync-anchor-poc.md](../../plans/height-sync-anchor-poc.md)`
§9.3 / §9.4 — protocol-level acceptance.
- `[devshard/testenv/scenarios/SCENARIOS.md](SCENARIOS.md)` — current
in-process scenarios (the contract this plan must preserve).
- `[devshard/testenv/README.md](../README.md)` — testenv runbook,
per-service env vars, `Makefile` targets.
- `[devshard/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md](../../docs/proposals/PROTOCOL_TESTING_PROPOSAL.md)`
— **the broader vision** for testenv-driven protocol E2E (cPoC, validation,
gossip, fault injection). This plan delivers the Go-side harness that
proposal expects.
- `[devshard/docs/proposals/CPOC_PROTOCOL.md](../../docs/proposals/CPOC_PROTOCOL.md)`
— cPoC skip protocol. The harness here is intentionally shaped so its
scenarios can land on the same infrastructure (see §10).
- Reference implementation: `[devshard/testenv/citest/stack_integration_test.go](../citest/stack_integration_test.go)`
— already drives `docker compose up -d --build` from `go test` with a
build tag, project name, and `t.Cleanup(down)`. The plan extends this
pattern; it does **not** introduce a new framework.

---

## 0. Goals (read this first)

This plan has **four** goals, in order of priority:

1. **Re-implement every height-sync E2E scenario** (currently in-process)
   on the real Docker Compose stack — see §1, §2, §5.
2. **Continuously validate the testenv platform itself.** Every scenario
   doubles as a smoke check that `gencompose`, `mockdapi`, `heightsyncd`,
   the observability pipeline (Alloy → Loki → VictoriaMetrics → Grafana),
   and the `devshardctl` proxy all work end-to-end. If a scenario fails
   for a reason unrelated to height-sync — e.g. Loki misses log lines,
   `mockdapi` does not reconnect, a host's `/healthz` never goes ready —
   that is a real testenv defect surfaced by the suite, not test flake.
   See §11 for the explicit "platform health" assertions every scenario
   inherits.
3. **Lay the foundation for more complex integration suites** —
   notably **cPoC** (`CPOC_PROTOCOL.md`) and **validations**
   (`MsgValidation`, `MsgValidationVote`, finalization).
   These suites do not exist yet; this plan deliberately picks
   harness shapes (control-plane fault injection hooks, scenario
   builder, escrow factory) that those suites can consume without
   re-inventing the bring-up / assertion path. See §10.
4. **Simulate a production-like environment with realistic randomness
   and exercise malware / cheater detection.** In production, hosts do
   **not** observe the chain in lock-step — there is per-host height
   skew (network jitter, validator-set partition, occasional SSE
   reconnects) that the protocol must distinguish from **deliberately
   malicious** hosts or clients (forged block hashes, sustained
   cheating trails, colluding peers, mutating clients). This goal
   makes both classes reproducible in the testenv and asserts that
   the detector signals (audit ring, dispute-bundle logs, mismatch
   counters) fire on malice but stay quiet on honest drift. See §12
   for the scenario set, fault knobs, and detection contract.

---

## 1. Why redo the E2E suite at the container level

The in-process `httptest`-backed tests (current `heightsync_anchor_e2e_test.go`)
fake three things the production stack actually provides:

1. `**heightsyncd`** — real HTTP/SSE block-oracle producer signed by the
  configured validator set.
2. `**mockdapi**` — real `blockoracle/client` SSE consumer with reconnect,
  `StaleAfter`, and host-trust mode.
3. `**mock-chain**` — gRPC bridge (escrow / participants / warm keys).

The in-process suite uses static / stopping `BlockOracle` Go values to
simulate (1)–(3). That is fast and deterministic but **does not** prove:

- The protobuf envelope round-trips through the real `devshardd-testenv`
binary (Echo router, Air-built executable, host signer keys filled by
`gencompose`).
- `mockdapi` / SSE actually translates a `docker stop height-sync` into
the oracle errors `AnchorScheduler.Decide` then turns into Omit.
- Logs land in **Loki** with the field names every scenario asserts on
(`mode`, `peer_block_hash_prefix`, `forced_start`, `peer_id`, …).
- Metrics that ops will rely on (Prometheus `up{}`, custom heightsync
counters) actually export.

A literal compose-driven suite closes that gap. It runs less often
(several minutes vs. 5 s for the in-process suite), but **both** suites
co-exist: in-process stays the inner loop; container is the gate.

---

## 2. Scope & non-goals

### 2.1 Scope

Three concentric scopes, each delivered alongside the height-sync work:

**A. Height-sync scenarios (primary, this plan).** Re-cover every scenario
from `SCENARIOS.md` against the compose stack:


| In-process test                                                             | Compose-equivalent test (proposed)                     | §9.3        |
| --------------------------------------------------------------------------- | ------------------------------------------------------ | ----------- |
| `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail`                         | `TestContainerE2E_HeightSync_Cadence`                  | 1–4         |
| `TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts`                  | `TestContainerE2E_HeightSync_CarriesHigherPeerTip`     | 4.1         |
| `TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing`                     | `TestContainerE2E_HeightSync_LostFirstResponse`        | 5           |
| `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`                       | `TestContainerE2E_HeightSync_ForceAnchorSingleMessage` | 6 (current) |
| (planned) `TestHeightSyncAnchor_E2E_ForcedSyncTurn_`*                       | `TestContainerE2E_HeightSync_ForcedSyncTurn_*`         | 6 (§5.5)    |
| `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`                 | `TestContainerE2E_HeightSync_CheatingTrail`            | 7           |
| `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors` | `TestContainerE2E_HeightSync_FeedStoppedOmits`         | 8           |
| `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns`    | `TestContainerE2E_HeightSync_FeedRecovers`             | 8           |
| (none today)                                                                | `TestContainerE2E_HeightSync_Smoke` (§9.4)             | 9.4         |


**B. Testenv platform validation (delivered for free).** Every scenario
asserts a small set of **platform invariants** before scenario-specific
assertions run. These are codified in `container/platform.go` (§11) and
exercise pieces that today are only verified by hand or by the operator
runbook:

- `gencompose` produced a valid `docker-compose.yml` (consumed at boot).
- `mock-chain` answers gRPC bridge queries.
- `heightsyncd` publishes `/block/latest` with `height > 0` and an
  SSE stream that increments.
- Every `devshardd-testenv-<i>` reaches `/healthz` and exports
  `/metrics`.
- Alloy ingests host logs into Loki tagged `subsystem=heightsync` and
  VictoriaMetrics scrapes the host counters added in §6.
- `devshardctl` proxy answers `/v1/status` and accepts inferences.

**C. Foundation for cPoC + validation E2E suites (forward-looking).**
The harness primitives this plan adds (control-plane fault injection,
scenario builder, escrow factory, log/metric assertions) are deliberately
**generic**: the height-sync suite is the first consumer, but **cPoC
skip** (`CPOC_PROTOCOL.md` cases C1–C14) and **validations** flows
must reuse them with minimal new code. The shape these primitives take
is described in §10; the actual cPoC / validation tests are out of scope
for this plan but **will be cheap to land afterwards** if §10 is followed.

**D. Production realism + malware/cheater detection (concrete scenarios
in this plan).** In addition to the deterministic scenarios in (A), the
suite includes a small set of "wide" scenarios that run the stack with
per-host oracle drift, per-host SSE jitter, and explicit malicious
actors (cheating hosts, colluding hosts, malicious client). These
scenarios assert that **detection signals** (audit ring entries with
`TrustPeerAligned=false`, mismatch counters, structured cheating-trail
logs) fire **only on malice** — honest drift must not produce false
positives. See §12.

### 2.2 Non-goals

- **Replacing** the in-process suite. The Go-only tests stay as the fast
developer loop. Container tests run on PR / nightly, not on every save.
- Verifying the **Strong** path (`light_block`, `VerifyCommit`,
`> D` escalation). Out of scope for the proof of concept; carry-forward malware  
detection is also tracked under "follow-ups" in `SCENARIOS.md`.
- Adding a brand-new test framework. We extend the existing
`devshard/testenv/citest` package and its `//go:build testenvci`
pattern.
- **Implementing** cPoC or validation scenarios in this plan. We only
guarantee the harness primitives (§10) are in place; the scenarios
themselves land as separate plans against `CPOC_PROTOCOL.md` and the
validation surface. `PROTOCOL_TESTING_PROPOSAL.md` describes those
suites at the proposal level.

---

## 3. Harness layout (proposed)

### 3.1 New package — `devshard/testenv/scenarios/container`

```
devshard/testenv/scenarios/
├── CONTAINER_E2E_PLAN.md          ← this document
├── SCENARIOS.md
├── heightsync_anchor_e2e_test.go  ← stays (in-process)
├── sanity_test.go                 ← stays
└── container/                     ← new
    ├── stack.go                   ← shared compose harness (re-uses citest helpers)
    ├── stack_test.go              ← TestMain: gen-compose + up + healthz
    ├── platform.go                ← platform invariants every scenario inherits (§11)
    ├── loki.go                    ← LogQL helper (§4.1)
    ├── metrics.go                 ← VictoriaMetrics PromQL helper (§4.2)
    ├── ctl.go                     ← devshardctl HTTP client (POST /v1/chat/completions)
    ├── escrow.go                  ← scenario-scoped escrow factory (one per Test*)
    ├── faults.go                  ← control-plane fault injection (§10.2)
    ├── scenario.go                ← fluent scenario builder (§10.3)
    ├── cadence_test.go            ← scenario 1–4
    ├── carry_forward_test.go      ← scenario 4.1
    ├── lost_first_response_test.go← scenario 5
    ├── force_anchor_test.go       ← scenario 6 (single-message + forced turn)
    ├── cheating_trail_test.go     ← scenario 7
    ├── feed_stopped_test.go       ← scenario 8
    └── smoke_test.go              ← §9.4
```

`platform.go`, `faults.go`, `escrow.go`, `scenario.go` are deliberately
**height-sync-agnostic**. They are the bricks the cPoC + validation
plans (§10) consume verbatim.

Build tag — all tests in this directory carry:

```go
//go:build testenvci
```

so a plain `go test ./...` ignores them. Same as `citest/`.

### 3.2 One stack per `go test` invocation

`stack_test.go::TestMain` runs **once** per `go test ./scenarios/container/...`:

1. `make gen-compose` (idempotent; ensures `docker-compose.yml` reflects
  `config.yaml`).
2. `docker compose -f docker-compose.yml -p ${TESTENV_PROJECT}-{pid} up -d --build`.
3. Wait for `**heightsyncd`** to publish `/block/latest` with `height > 0`
  and every `devshardd-testenv-<i>` to expose its `/healthz` (or the
   existing readiness check, see `citest`).
4. Register `t.Cleanup(down)` via `m.Run` wrapper to **`docker compose
  down --remove-orphans --timeout 60`**.

Per-scenario tests **share** the stack but each scenario opens a fresh
**escrow** (`devshardctl` builds a session bound to `ESCROW_ID`; new
escrows start at nonce `1`). Scenarios never destructively reset
`**heightsyncd`** state except the dedicated feed-stopped tests, which
restore it before returning.

`go test -timeout=20m -run '^TestContainerE2E_'` is the canonical command.

### 3.3 Reusing `citest` helpers

`citest/stack_helpers.go` already implements:

- `up(t, composeFile, project)` / `down(...)`,
- `getHeightFromLatest(...)`,
- generic HTTP polling.

The plan extracts these into an exported sub-package
`**devshard/testenv/citest/harness**` and the new `container/` tests
import them. No duplication. `TestStackIntegrationI1andSection8_7`
keeps its own narrow scope (I1, I2a, I2b, I9, §8.7 wiring) — it is **not**
collapsed into the height-sync suite.

---

## 4. Verification primitives

In-process tests poke `*transport.Server.HeightSyncAuditRing()` and a
`captureLogger` directly. Containerised tests cannot — no shared
process. Three replacement primitives, all already wired in compose:

### 4.1 Loki LogQL (primary — log-based assertions)

The existing observability stack ships **Alloy → Loki → Grafana**
(see `devshard/testenv/observability/`). Every `logging.Debug/Info/Warn`
on the `heightsync` subsystem already lands in Loki tagged
`subsystem=heightsync`. A scenario asserts via:

```
GET http://127.0.0.1:3100/loki/api/v1/query_range?
    query={subsystem="heightsync", direction="request"} | json
   &start=<scenario_start_ns>&end=<now_ns>
   &limit=1000
```

Helper signature in `loki.go`:

```go
type LokiQuery struct {
    LogQL  string
    Start  time.Time
    End    time.Time
    Limit  int
}

type LokiEntry struct {
    Time    time.Time
    Message string
    Fields  map[string]string // parsed from JSON line
}

func QueryLogs(t *testing.T, q LokiQuery) []LokiEntry
```

**Field map.** Same keys the in-process tests already scrape, e.g.
`mode`, `direction`, `peer_block_hash_prefix`, `block_hash_prefix`,
`peer_id`, `host_id`, `nonce`, `forced_start`, `forced_end`, `delta`,
`local_aligned`, `trust_level`. We pin field names in
`devshard/heightsync` and `devshard/transport` today; the
container plan **also** locks them as Loki contract — see §6.

### 4.2 VictoriaMetrics / PromQL (secondary — counters)

`devshardd-testenv` exports Prometheus on `:9600` (env `EXPORT_METRICS=1`,
`METRICS_PORT=9600`). VM scrapes every host. Scenario suite adds **light
heightsync counters** before phase 1 ships:


| Metric                                                   | Labels                                  | Where                                                          |
| -------------------------------------------------------- | --------------------------------------- | -------------------------------------------------------------- |
| `devshard_heightsync_outbound_anchors_total`             | `direction` (`request` or `response`), `escrow_id`, `host_id` | `transport.HTTPClient`, `transport.Server`                     |
| `devshard_heightsync_inbound_anchors_total`              | `direction`, `trust_level`, `escrow_id` | `transport.Server`                                             |
| `devshard_heightsync_force_request_anchor_missing_total` | `escrow_id`, `host_id`                  | `transport.Server.recordForceRequestAnchorMissingIfApplicable` |
| `devshard_heightsync_oracle_failures_total`              | `host_id`                               | `transport.Server.latestOracleHeader`                          |


Helper:

```go
func PromQuery(t *testing.T, expr string) float64       // single-vector value
func PromVector(t *testing.T, expr string) []PromSample // labeled samples
```

These metrics are **new code** (not yet present); they are tracked as
explicit deliverables in §7.1. They make every scenario's assertions
mechanical (`+4` request anchors over `nonces 1..4` becomes a
PromQL `increase(...) == 4`).

### 4.3 Container exec (tertiary — escape hatch)

For state that is not in logs/metrics today (e.g. inspecting the host
SQLite store or directly reading the audit ring), the harness uses

```go
docker compose -f docker-compose.yml exec -T <svc> <cmd>
```

via `os/exec`. Used sparingly — every reach for `exec` is a signal we
should add a debug HTTP route or a metric instead. The plan tracks two
likely additions in §7.2.

---

## 5. Per-scenario test design

Each scenario follows the same shape:

1. **Arrange** — open a fresh escrow on `devshardctl` (POST
  `/v1/inference` with new `escrow_id`) and capture `t0 := time.Now()`.
2. **Act** — drive traffic by POSTing inferences to
  `http://127.0.0.1:8081/v1/chat/completions` (devshardctl proxy) — see
   `cmd/devshardctl/proxy.go`.
3. **Wait** — Loki ingestion lag is ≤ 2 s in steady state; assertions
  poll for up to 10 s.
4. **Assert** — Loki LogQL + PromQL increases, vs. baselines captured at
  step 1.

### 5.1 `TestContainerE2E_HeightSync_Cadence` (§9.3 1–4)

- **Setup**: standard 4-host stack, `K=8`, `slots=4`. `heightsyncd`
serves the static validator set from `config.yaml`.
- **Drive**: 16 inferences via `devshardctl`.
- **Assert**:
  - Loki: count of `{subsystem="heightsync", direction="request", mode="anchor"}` since `t0` is **9** (`{1..4} ∪ {8..11} ∪ {16}`),
  count of `mode="omit"` is **7**.
  - Per-host: same query with `host_id=<i>` matches the round-robin
  subset (depends on first-served slot — derive deterministically).
  - PromQL: `increase(devshard_heightsync_outbound_anchors_total {direction="request"}[2m])` returns **9**.
  - Audit-ring bytes parity: parse `block_hash_prefix` and
  `peer_block_hash_prefix` from logs and assert equality at every
  `mode=anchor` nonce.

### 5.2 `TestContainerE2E_HeightSync_CarriesHigherPeerTip` (§9.3 4.1)

- **Setup**: spin up a second `heightsyncd` instance in a side compose
override that publishes `(H+1, hash')` and route **only host-2**'s
`HEIGHT_SYNC_URL` to it. The base compose generator already supports
per-host env vars (`gencompose` writes them); the harness adds an
override compose file `docker-compose.scenario-mixed-height.yml`
that swaps `height-sync` for host-2.
- **Drive**: nonces 1..4.
- **Assert**: Loki entries for the user emit show `block_hash_prefix`
switching from `hash` to `hash'` after host-2's first response;
hosts 3–4 receive `peer_block_hash_prefix=hash'`.

> **Implementation cost.** The override file is the price for
> heterogeneous hosts at compose level; without it, every host shares
> one `height-sync` service. Acceptable because it is a single
> scenario.

### 5.3 `TestContainerE2E_HeightSync_LostFirstResponse` (§9.3 5)

- **Setup**: standard stack.
- **Drive**: send nonce 1 inference, immediately
`docker compose -f docker-compose.yml stop devshardd-testenv-X`
where `X` is the round-robin target. Send nonce 2.
- **Assert**: nonce 2 still emits Anchor (Loki query `mode=anchor`),
nonce 1 reports a transport error in `devshardctl` proxy logs, by
nonce 4 the user audit ring has at least one host attestation
(verified via PromQL — `devshard_heightsync_inbound_anchors_total`
on the **client side** which we add for symmetry).
- **Cleanup**: `docker compose start devshardd-testenv-X`.

### 5.4 `TestContainerE2E_HeightSync_ForceAnchorSingleMessage` (§9.3 6, current)

- Equivalent to `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`,
except the `ForceHeightSyncAnchor: true` flag must travel through
the `devshardctl` HTTP proxy. **Action item:** extend
`cmd/devshardctl/proxy.go` to accept a `force_height_sync_anchor`
field on its inference body; or add a query parameter
`?force_height_sync=true`. Tracked in §7.1.

### 5.5 `TestContainerE2E_HeightSync_ForcedSyncTurn_*` (§9.3 6, §5.5)

Mirrors Scenarios A–D in `SCENARIOS.md` once `MsgForceHeightSyncTurn`
is implemented. Honest-user variants assert host-side response anchors
via `devshard_heightsync_outbound_anchors_total{direction="response"}`
deltas of `+1` per host across the window. Malicious-user variant
asserts `devshard_heightsync_force_request_anchor_missing_total`
non-zero per host **and** zero `inbound_anchors_total{direction="request"}`
delta over the window. No additional compose work beyond §5.4.

### 5.6 `TestContainerE2E_HeightSync_CheatingTrail` (§9.3 7)

The in-process test injects via `transport.ClientConfig.HeightSyncRequestMutateHook`.
That hook is **deliberately** not exposed by `devshardctl`. Two options:

- **A (preferred):** add a tiny `devshardctl` debug endpoint
`POST /v1/debug/cheat-anchor` (built only with `-tags devshardctl_debug`
or gated by env `DEVSHARDCTL_DEBUG=1`) that sets one shot of the
mutate hook for the next inference.
- **B:** rely on the in-process test alone; document it as
*not container-equivalent* in `SCENARIOS.md`.

This plan picks **(A)** because it preserves the §9.3 acceptance
phrasing ("inject a modified `mainnet_block_hash` for one outgoing user
request"). Tracked in §7.2.

Assertions: Loki query for the inbound `peer attestation received`
event whose `peer_block_hash_prefix` differs from the height the same
host's debug logs report from its own oracle, and trust label is
`peer_aligned` (same height, different hash).

### 5.7 `TestContainerE2E_HeightSync_FeedStopped*` (§9.3 8)

- `**TestContainerE2E_HeightSync_FeedStoppedOmits`**:
  1. Send nonce 1 (Anchor).
  2. `docker compose -f docker-compose.yml stop height-sync`.
  3. Wait for `mockdapi` `StaleAfter` (default 10 s — config-tuned to 3 s
    for tests via `MOCKDAPI_STALE_AFTER=3s` env override on each host;
     the harness sets it before `compose up`).
  4. Send nonce 2 — assert Loki `mode=omit` on user emit, host inbound,
    and user outbound; PromQL
     `increase(devshard_heightsync_oracle_failures_total[1m])` ≥ 1.
  5. Inference HTTP body returns 200; receipt is delivered.
- `**TestContainerE2E_HeightSync_FeedRecovers**`:
  1. `docker compose start height-sync`.
  2. Send nonce 8 — assert Loki `mode=anchor` and PromQL counter
    resumes climbing.

### 5.8 `TestContainerE2E_HeightSync_Smoke` (§9.4)

A thin wrapper: run the `Cadence` scenario end-to-end and assert that
**at least one** Anchor was observed (any direction). Doubles as the
shell helper §9.4 asks for, but written in Go for consistency with the
rest of the suite. The Makefile target `make e2e-smoke` shells out to
`go test -tags=testenvci -run '^TestContainerE2E_HeightSync_Smoke$' ./testenv/scenarios/container/...`.

---

## 6. Wire / log contract additions

To keep the plan landable in small slices, **lock the field names** the
container suite asserts on. Each entry below is a deliverable:

1. **Logging.** Audit `devshard/transport/server.go` and
  `devshard/transport/client.go` so every `heightsync:` log line has
   stable keys: `direction`, `mode`, `nonce`, `peer_id`, `host_id`,
   `block_hash_prefix`, `peer_block_hash_prefix`, `forced_start`,
   `forced_end`, `trust_level`, `local_aligned`, `delta`. Move these
   into a small constants file (`devshard/heightsync/logfields.go`)
   so future drift is caught at compile time.
2. **Metrics.** Add the four counters listed in §4.2 — initial
  implementation can be a single `prometheus.Counter` per series with
   `prometheus.NewCounterVec(...)`. Wire them at the same call sites
   the logging lives at.
3. `**/healthz`.** Confirm `devshardd-testenv` has a `/healthz` (it
  does, see `transport`). If not present, add one returning
   `{height_sync_oracle_height, last_anchor_at}`.

---

## 7. Deliverables and phasing

Land in this order so each phase is independently reviewable.

### 7.1 Phase A — harness + cadence (covers §9.3 1–4)

- New package `devshard/testenv/scenarios/container/` with
`stack_test.go` (TestMain) and `cadence_test.go`.
- Promote `citest/stack_helpers.go` to `citest/harness/` and switch
`citest/stack_integration_test.go` to import from there.
- Add §6 deliverables 1 and 2 (log fields and outbound/inbound anchor
counters).
- Extend `cmd/devshardctl/proxy.go` to accept `force_height_sync_anchor`
on the inference JSON.
- Makefile: `make e2e` → `go test -tags testenvci -timeout=20m ./testenv/scenarios/container/...`.

### 7.2 Phase B — recovery + force + cheating-trail (covers 5, 6 current, 7)

- `lost_first_response_test.go`,
`force_anchor_test.go` (single-message),
`cheating_trail_test.go`.
- Add `cmd/devshardctl` debug endpoint behind `DEVSHARDCTL_DEBUG=1`
for the bogus-hash injection (§5.6 option A).
- Add `oracle_failures_total` metric (§4.2).

### 7.3 Phase C — feed stopped (covers 8) + smoke (§9.4)

- `feed_stopped_test.go` and `smoke_test.go`.
- Wire the `MOCKDAPI_STALE_AFTER` env knob through `devshardd-testenv`
→ `mockdapi.Config.StaleAfter` so tests can shorten the
oracle-stale window without bloating real boot times.

### 7.4 Phase D — forced sync turn (covers 6 §5.5)

- Lands **after** `MsgForceHeightSyncTurn` (Step 9 of the PoC plan)
is implemented.
- `forced_sync_turn_test.go` covers Scenarios A–E from
`SCENARIOS.md`. Honest + malicious users.

### 7.5 Phase E — mixed-height carry-forward (covers 4.1)

- `carry_forward_test.go` plus the
`docker-compose.scenario-mixed-height.yml` override file.
- Cleanup hook stops the second heightsyncd instance even on
failure.

---

## 8. CI integration

- **Local:** `make e2e` runs Phase A scenarios on a developer box. All
scenarios use the same compose project so `make down` after the run
is sufficient cleanup.
- **CI:** GitHub Actions job `testenv-ci-heightsync` runs
`go test -tags testenvci -timeout=25m ./testenv/scenarios/container/...`
on `ubuntu-latest` with a 4-CPU runner. Cold-build cost is
≈3 min; subsequent runs hit Docker layer cache. Job runs on
push-to-main and on the `testenv-`* PR label.
- **Parallelism:** scenarios serialize on the shared compose stack via
`t.Parallel()` *opt-in* — Phase A runs serially; Phase B/C/D can be
parallelised once we add per-test escrow rotation.

---

## 9. Risks and open questions

- **Loki query lag.** Default Alloy push interval is 2 s. Assertions
poll for up to 10 s; failure modes that present as "missing logs"
will look like real bugs. Decision: helper retries until a hard
budget (default 15 s) elapses, then fails with the actual current
count.
- **Compose override files for heterogeneous hosts.** §5.2 needs one;
if `gencompose` ever rewrites the YAML out from under a test run,
the override must be additive (Compose merges by file order).
- **Test-only env knobs.** `MOCKDAPI_STALE_AFTER`,
`DEVSHARDCTL_DEBUG`, `EXPORT_METRICS=1` (already set in dev)
are not real-prod knobs; document them in `README.md` §3.2 once
Phase A lands so future readers do not mistake them for protocol
config.
- `**docker compose stop`/`start` semantics.** Stopping
`height-sync` keeps the container's IP, so `mockdapi` SSE clients
see TCP RST then reconnect. Verify behaviour matches a real outage
before relying on it for §9.3 item 8 (Phase C task).
- **Force-anchor field on the proxy.** §5.4 requires a tiny API
surface change on `cmd/devshardctl/proxy.go`. If we want to keep
the proxy "production-shape," gate the new field behind
`DEVSHARDCTL_DEBUG=1` and document it explicitly.

---

## 10. What this enables next — cPoC + validation suites

The height-sync E2E suite is **the first** consumer of the container
harness. The same harness is intentionally shaped to host two more
suites that already have proposals but no Go-side scaffolding:

- **cPoC skip protocol** — `[CPOC_PROTOCOL.md](../../docs/proposals/CPOC_PROTOCOL.md)`
  cases C1 → C14 (honest skip, fake skip, double-claim, late carry,
  forged carry, refusal probe, low-load gossip, high-load elision,
  dispute bundle, etc.).
- **Validations** — `MsgValidation` + `MsgValidationVote` flows,
  validator selection, finalization-bundle assembly, and the negative
  cases the in-process state-machine tests cover at unit level
  (invalid validator, missing votes, duplicate votes, late finalize).

`PROTOCOL_TESTING_PROPOSAL.md` describes both suites at the proposal
level. This plan does **not** implement them, but it commits to
shipping the four primitives below so each new suite is "write a test"
rather than "design a harness".

### 10.1 Escrow & session factory (`container/escrow.go`)

Today scenarios share a single escrow because `devshardctl` boots
once per stack. The harness exposes:

```go
// Returns a freshly minted escrow_id + bound user.HostClient session.
// The factory talks to mock-chain to mint the escrow and to
// devshardctl /v1/escrows to register it.
func NewScenarioEscrow(t *testing.T, opts ...EscrowOpt) *ScenarioEscrow

type EscrowOpt func(*escrowConfig)
// e.g. WithBalance, WithExecutor, WithValidatorSet, WithRefusalRules.
```

This is what cPoC suites need: each `Cn` case in `CPOC_PROTOCOL.md`
is one developer + one host pair acting on a fresh escrow with a known
nonce window. The factory must also return an `EscrowAuditor` that
exposes the host's audit ring (over the existing diff transport) so
tests can assert "developer evidence stored verbatim".

### 10.2 Control-plane fault injection (`container/faults.go`)

Mirrors `PROTOCOL_TESTING_PROPOSAL.md` §6.2 ("subnethost") on the Go
side. A fault is a typed configuration sent to a single host's
**fault-injection endpoint** (`/v1/_test/faults`, gated by build tag
`testenvci` so it never reaches production binaries):

```go
type Fault interface{ apply(host string) }

// Network / liveness
type FaultPause struct{ Until time.Duration }
type FaultDrop  struct{ Path string; Match func(http.Request) bool }
type FaultDelay struct{ Path string; Min, Max time.Duration }

// Protocol-level (cPoC)
type FaultRefuseInference   struct{ Window NonceWindow; Reason string }
type FaultDoubleClaim       struct{ InferenceID string }      // C2'
type FaultForgedCarry       struct{ InferenceID string }      // C3'
type FaultMissingValidation struct{ InferenceID string }      // validation
type FaultInvalidVote       struct{ Voter string; Vote types.MsgValidationVote }
```

Faults are **scoped to a single test** and unwound in `t.Cleanup` via
`/v1/_test/faults/clear`. The height-sync feed-stopped suite (§5) is
the first consumer (`FaultPause` against `heightsyncd`); cPoC and
validation suites consume the rest.

Concretely the height-sync plan must add:

- `/v1/_test/faults` endpoint to `devshardd-testenv` (build-tag-only).
- A small dispatcher in `host` that consults the registered faults
  before each `transport.Server` handler runs. (Already partially
  modeled by the existing `*Hook` knobs in `transport/client.go`,
  `transport/server.go` — those become the production-internal
  expression of the same idea.)

### 10.3 Scenario builder (`container/scenario.go`)

The pattern that recurs in `heightsync_anchor_e2e_test.go` —
`setupFourHostHTTPHeightSync*` followed by N `client.Send(...)` calls
followed by Loki/audit-ring assertions — is captured as a fluent
builder so cPoC / validation tests do not re-write 60 lines of stack
wiring per case:

```go
NewScenario(t, stack).
  Escrow(WithExecutor("host-0"), WithValidators(2)).
  Fault("host-1", FaultRefuseInference{Window: NonceWindow{From: 3, To: 5}}).
  Inference(Nonce(3), TraceTag("c2-fake-skip")).
  ExpectAudit("host-0", AuditEntryHasFlag(audit.TrustPeerAligned)).
  ExpectLogki(`{subsystem="cpoc"} |~ "fake_skip_detected"`).
  ExpectMetric(`devshard_cpoc_fake_skip_total{host="host-0"} == 1`).
  Run()
```

The scenarios in §5 of this plan are re-expressible in this form; we
will keep them table-driven so the height-sync suite **proves the
builder works** before cPoC consumes it.

### 10.4 Observability contract for cPoC + validation

The wire/log additions in §6 are height-sync-only. Two more contract
chunks must land **before** the corresponding suites can be written;
this plan does not commit to landing them, but it documents them now
so the schema does not drift:

- **cPoC log contract** — the host emits structured events keyed by
  `subsystem=cpoc`: `cpoc_skip_emitted`, `cpoc_skip_observed`,
  `cpoc_fake_skip_detected`, `cpoc_double_claim_detected`,
  `cpoc_late_carry`, `cpoc_vote_emitted`, `cpoc_vote_collected`,
  with an `inference_id` plus the nonces named in `CPOC_PROTOCOL.md`
  §"Notation".
- **Validation log/metric contract** — `subsystem=validation` events
  for `validation_started`, `validation_finished`,
  `validation_vote_in`, `validation_vote_invalid`,
  `validation_round_finalized`; PromQL counters
  `devshard_validation_votes_total{outcome=...}` and
  `devshard_validation_round_latency_seconds`.

When the cPoC plan lands, it inherits §4.1 / §4.2 helpers without
modification — only the queries change.

### 10.5 What this plan must NOT pre-empt

- It will **not** add cPoC- or validation-specific code paths to host
  / state. Those land with their own plans.
- It will **not** assert anything about cPoC fields in §5; the tests
  that touch §10.x land later.
- The fault-injection endpoint (§10.2) ships with **only** the
  `FaultPause` / `FaultDrop` / `FaultDelay` cases this plan needs
  for height-sync. Protocol-level faults are stubs that return 501
  until their suite picks them up. This keeps the surface area small.

---

## 11. Platform invariants every scenario inherits

Each `TestContainerE2E_*` runs `container.AssertPlatformReady(t, stack)`
at the top, before any scenario logic. This is goal **B** from §0:
the suite continuously validates the testenv platform itself, so a
green scenario is **also** a green platform check.

`AssertPlatformReady` checks, in order:

1. `docker compose ps` reports every service in `running (healthy)`.
2. `mock-chain` answers `GET /v1/health` (gRPC bridge ready).
3. `heightsyncd` `GET /block/latest` returns `height > 0`; the SSE
   stream emits at least one event within `2 × block_interval`.
4. Every `devshardd-testenv-<i>` `GET /healthz` is 200 and `GET
   /metrics` exposes `devshard_heightsync_anchor_total`.
5. Loki `/loki/api/v1/query` for `{subsystem="heightsync"}` returns at
   least one log line within the last 30 s (proves Alloy ingestion).
6. VictoriaMetrics `/api/v1/query` for `up{job=~".+devshardd.+"}` is
   `== 1` for every host.
7. `devshardctl` `GET /v1/status` returns 200 with the expected
   participant count.

A failure of (1)–(7) is treated as a **testenv defect** and surfaced
with a distinct error type (`*PlatformError`) so triage can
distinguish "scenario-found-a-protocol-bug" from "platform-broken".
The `make e2e` target (see §7) re-runs the suite once if the failure
is `*PlatformError` and the run was the first attempt — this absorbs
genuine cold-boot races on slow CI runners without hiding real flakes.

---

## 12. Production realism & malware / cheater detection

This section satisfies goal **4** from §0. The deterministic scenarios
in §5 keep oracle state lock-step across all four hosts so the protocol
under test is the only variable. Real deployments are nothing like
that: each host has its own SSE consumer, its own reconnect history,
and (in the threat model) a non-zero probability of being **dishonest**
or sharing requests with a **dishonest client/developer**. This
section introduces:

- A **per-host oracle stream** so different hosts can observe slightly
  different latest heights / hashes (legitimate) or **fabricated**
  ones (malicious).
- A **production-realism profile** (jitter, brief disconnects, drift)
  that runs without any malicious actor — the suite asserts the
  protocol stays clean.
- A **malicious-actor profile** layered on top of (or instead of) the
  realism profile that asserts the detector signals fire.
- A **detection contract** (logs, audit-ring entries, counters) that
  every malware scenario asserts on, and that honest scenarios assert
  remains silent.

### 12.1 Per-host oracle drift — design choice

The in-process suite uses one `staticOracle` shared by all four hosts.
For container realism we need each host to consume a feed it can drift
or corrupt independently. Two viable shapes (the plan picks **B**):

**A. N independent `heightsyncd` instances.** `gencompose` emits one
`heightsyncd-<i>` per host plus per-host `HEIGHT_SYNC_ENDPOINT`. Each
instance accepts a launch-time config `HEIGHT_SYNC_OFFSET=Δ` and
`HEIGHT_SYNC_JITTER_MS=σ`. **Pros:** closest to production wire
shape. **Cons:** quadruples container count, multiplies validator-set
key handling, and slows boot.

**B. One `heightsyncd` + per-host `mockdapi` mutator.** Single
producer; each `devshardd-testenv-<i>` runs a build-tag-only middleware
inside `mockdapi` that applies a configurable transform to every SSE
event before it reaches `AnchorScheduler.Decide`:

```go
type OracleTransform struct {
    HeightOffset int64         // ±N blocks of legitimate drift
    JitterMin    time.Duration // pre-deliver delay range
    JitterMax    time.Duration
    DropEvery    int           // simulate brief reconnect (drop 1 in N)
    HashOverride func(height uint64) []byte // malicious — fabricate hash
}
```

Configured via the same fault endpoint defined in §10.2:
`POST /v1/_test/oracle-transform` (gated by `testenvci`). **Pros:** no
extra containers; per-host config flips at runtime; maps cleanly to
the cheating-trail tests. **Cons:** the SSE wire itself is shared, so
"validator set partition" is **not** modeled (called out as future
work in §13).

The plan adopts **B** for height-sync E2E. Option **A** can be added
later if a scenario explicitly needs a partitioned producer (e.g. some
cPoC dispute cases per `CPOC_PROTOCOL.md`).

### 12.2 Realism profile (no malice — must stay green)

`TestContainerE2E_HeightSync_RealismHonest` runs the four-host stack
under a baseline production-like profile applied to every host:

- `HeightOffset` chosen uniformly from `{-1, 0, +1}` per host at boot.
- `JitterMin = 50 ms`, `JitterMax = 250 ms`.
- `DropEvery = 30` (≈3% events skipped, simulating a flaky link).

Run a 2-minute burst of inferences (≥ 5 cadence ticks) under
`devshardctl`. Assertions:

- **No malicious detector fires.** Audit-ring entries with
  `trust=peer_aligned` only; **zero** entries with
  `trust=peer_misaligned` or `trust=force_request_anchor_missing`.
- Counter `devshard_heightsync_peer_hash_mismatch_total` stays at `0`.
- Counter `devshard_heightsync_peer_height_drift_blocks_total` is
  **non-zero** (proof drift was actually exercised), and the per-host
  histogram `devshard_heightsync_peer_height_drift_blocks` reports
  values within `[-1, +1]`.
- Cadence completes per host; Anchor / Omit ratios are within ±20%
  of the deterministic suite.
- Inference success rate is `100%`.

This scenario is the strongest evidence that the **protocol** does
not over-trigger on legitimate noise; it is also the most likely
scenario to surface flaky platform behavior (Loki gap-fills, SSE
reconnects), which feeds back into goal 2.

### 12.3 Malice profiles (must trigger the detector)

`TestContainerE2E_HeightSync_MaliciousHost_BogusHashSustained`
configures host-1 with `HashOverride = func(h) → fabricated[h]` for
every Nth event (sustained version of §5.6 / point 7). Other hosts run
the realism profile from §12.2. Assertions:

- After cadence × 3, audit ring on every honest host contains
  `Q ≥ 1` entries with `trust=peer_misaligned` and
  `peer_id=host-1/...` and `peer_block_hash_prefix !=
  local_block_hash_prefix`.
- Counter `devshard_heightsync_peer_hash_mismatch_total{peer="host-1"}`
  is `≥ Q` on at least one honest host.
- Loki query
  `{subsystem="heightsync"} |~ "cheating_trail_recorded" | json
  peer_id="host-1*"` returns `≥ Q` events.
- Honest peers continue to produce successful inferences (detection
  is **separable** from progress).

`TestContainerE2E_HeightSync_MaliciousHosts_Colluding` configures
host-1 **and** host-2 with the **same** `HashOverride` (a coordinated
fake). Assertions:

- Each non-colluding honest host (`host-0`, `host-3`) records the
  cheating trail against **both** colluders independently. (No
  quorum-style masking — collusion does not silence detection.)
- Counter
  `devshard_heightsync_peer_hash_mismatch_total{peer=~"host-1|host-2"}`
  is non-zero on `host-0` and `host-3`.

`TestContainerE2E_HeightSync_MaliciousClient_MutatedRequest` reuses
the existing `HeightSyncRequestMutateHook` (§5.6) but exercises it
against the **container** stack: client (run inside `devshardctl`
with a build-tag-only fault) sends bogus `mainnet_block_hash` at the
correct height for a sustained burst of inferences. Assertions:

- Server-side audit ring records the bogus user hash with
  `direction=request` and `peer_id=client/...`.
- The request still succeeds (per the cheating-trail spec — we record,
  not refuse).
- Counter `devshard_heightsync_request_hash_mismatch_total` is
  non-zero.
- Honest hosts neither propagate nor accept the bogus hash as their
  own oracle truth (cross-checked via the audit ring on each host).

### 12.4 Detection contract — what the suite asserts on

These signals must exist (some already do; ones not yet emitted are
required additions, listed under §6 deliverables):

| Signal                                                    | Source        | Today | Add by this plan |
| --------------------------------------------------------- | ------------- | ----- | ---------------- |
| Audit ring `trust=peer_aligned`                           | `audit.go`    | yes   | —                |
| Audit ring `trust=peer_misaligned` (proposed)             | `audit.go`    | no    | yes (§6 add)     |
| Audit ring `trust=force_request_anchor_missing`           | `audit.go`    | yes   | —                |
| Log `cheating_trail_recorded` (json: peer_id, direction, peer_block_hash_prefix, local_block_hash_prefix) | host log | yes  | extend with `direction`, `peer_id`, mismatch reason |
| Counter `devshard_heightsync_peer_hash_mismatch_total{peer}`   | metric  | no   | yes (§6 add)    |
| Counter `devshard_heightsync_request_hash_mismatch_total`      | metric  | no   | yes (§6 add)    |
| Histogram `devshard_heightsync_peer_height_drift_blocks{peer}` | metric  | no   | yes (§6 add)    |

The realism scenario (§12.2) asserts the first column is **silent**
on the malicious-only signals; the malice scenarios (§12.3) assert
they fire. Together they pin down the false-positive / false-negative
boundary.

### 12.5 What this enables for cPoC + validation

The **same** `OracleTransform` plus per-host fault dispatcher (§10.2)
is what cPoC scenarios will use to inject **fake skip** (`C2`),
**double claim** (`C2'`), **forged carry** (`C3'`), and **late carry**
(`C3`) — all of which boil down to "produce a wire-level signal that
does not match the local oracle truth and assert detection". The
detection contract above generalizes one-to-one to the cPoC counters
named in §10.4. This is the concrete payoff of co-locating goals 3
and 4: cPoC malice-detection tests will not need a new harness layer.

### 12.6 Out-of-scope variants (called out for clarity)

- **Validator-set partition.** Requires shape **A** from §12.1; not
  modeled here. Add when a scenario actually needs it.
- **Adaptive malware.** Cheater that flips strategy based on observed
  detection signals. Belongs to a future security suite.
- **Network-layer attacks** (TLS / signature forgery). Out of scope
  — the wire is HTTP behind a known docker network in testenv.

---

## 13. Out-of-scope (future)

- **§9.3 item 1 short-block stack** (`block_interval_delta: 1s`) is
already tunable via `config.yaml`; no plan-level work, just CI
configuration when we want even faster scenarios.
- **Strong / `> D` escalation** — separate PoC, not relevant to
Anchor-only suite.
- **Multi-escrow / multi-user concurrency** — handled by existing
Phase 8 protocol stress tests, not by this suite.
- **cPoC scenario implementations** — covered by §10 primitives but
written under a separate plan against `CPOC_PROTOCOL.md`.
- **Validation scenario implementations** — same; relies on §10
primitives plus the validation log/metric contract (§10.4).
- **Validator-set partition / N independent `heightsyncd` instances**
(§12.1 option A) — only if a scenario needs it.
- **Adaptive malware** (§12.6) — separate security suite.

---

## 14. Acceptance — when is the plan done?

- `go test -tags testenvci ./testenv/scenarios/container/...` passes
on a clean Docker host with **all** §9.3 1–8 + §9.4 covered.
- `container.AssertPlatformReady` (§11) passes for every scenario
and is invoked from a single shared helper (no copy/paste).
- The four "foundation" files exist with at least the height-sync
subset implemented and TODO markers for cPoC / validation:
`platform.go`, `faults.go`, `escrow.go`, `scenario.go`.
- The realism + malice scenarios from §12 (`*_RealismHonest`,
`*_MaliciousHost_BogusHashSustained`, `*_MaliciousHosts_Colluding`,
`*_MaliciousClient_MutatedRequest`) are green, and §12.4's
detection contract is wired (counters export, audit ring trust
states emitted).
- `SCENARIOS.md` is updated so each scenario points to **both** its
in-process test and its container counterpart, and includes a new
"Production realism & malware detection" section mirroring §12.
- `README.md` §4 has a runbook entry for `make e2e`.
- The PoC implementation status file (`devshard/plans/height-sync-anchor-poc-implementation-status.md`)
flips Step 7 from "in-process" to "in-process + container" and
drops the §9.4 smoke open item.
- A short follow-up note is added to `CPOC_PROTOCOL.md` and (when it
lands) to the validation plan, confirming they can build on
`container/{escrow,faults,scenario,platform}.go` without changes.

