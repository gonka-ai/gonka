# E2E scenario catalog (`devshard/testenv/scenarios`)

Runnable acceptance tests for the height-sync protocol (Anchor / courier /
`(C-quorum)` / asymmetric response signatures). Normative behaviour:
[`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](../../docs/proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md).
Full test matrix (unit + e2e + planned Strong):
[`height-sync-tests.md`](../../docs/height-sync-tests.md).

This file is the **runbook**: what each scenario proves and how to execute it.
The test catalog is the authoritative index of test names and protocol-section
coverage.

**Two execution modes**

| Mode | Stack | Speed | When to use |
|------|--------|-------|-------------|
| **In-process** | Four `httptest` hosts + static `BlockOracle` | Seconds | Daily development; full v2 coverage (courier, confirmation, origin sig) |
| **Container** | Real `docker compose`: `heightsyncd`, `mockdapi` SSE, Loki, VictoriaMetrics | Minutes | CI gate; ops-faithful checks for **v1 baseline** (cadence, feed, cheating, …) |

Container rollout: [`CONTAINER_E2E_PLAN.md`](CONTAINER_E2E_PLAN.md) §7.

**Shared cadence config** (most scenarios): four hosts, `K = 8`,
`slots_num = 4` → initial sync turn on nonces `1..4`, periodic turns at
`8..11`, `16..19`, …; Omit between turns (`5..7`, `12..15`, …).

### Are container tests still relevant?

**Yes.** The seven `TestContainerE2E_HeightSync_*` tests are **not** obsolete.
They prove the same **baseline** behaviours as the in-process suite against a
real `heightsyncd` + `mockdapi` stack (protobuf through `devshardd-testenv`,
SSE stale semantics, Loki/Prom assertions). They do **not** yet cover v2-only
features (courier bootstrap, `(C-quorum)`, lazy carry, freshness `F`, response
origin signatures). That gap is **Phase D** in the container plan; Strong mode
is **Phase E**. Until Phase D lands, treat in-process e2e as the gate for v2
and container e2e as the gate for deployability of the shared stack.

### Implementation status — protocol v2 (in-process + container)

Legend: **✅** implemented · **❌** not implemented · **—** N/A for that tier.

| ID | Protocol area | In-process | Container |
|----|---------------|------------|-----------|
| — | Cadence + audit trail | ✅ `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail` | ✅ `TestContainerE2E_HeightSync_Cadence` |
| — | Cross-host higher tip | ✅ `…_CarriesHigherPeerTipAcrossHosts` | ❌ Phase D / §7.5 |
| — | Lost-first-response self-heal | ✅ `…_LostFirstResponseSelfHealing` | ✅ `TestContainerE2E_HeightSync_LostFirstResponse` |
| — | Force single message | ✅ `…_ForceAnchorOutsideSyncTurn` | ✅ `TestContainerE2E_HeightSync_ForceAnchorSingleMessage` |
| — | Forced sync turn (host Anchors if user Omits) | ✅ `…_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits` | ❌ Phase D |
| — | Cheating trail | ✅ `…_CheatingTrailStoresBogusUserHash` | ✅ `TestContainerE2E_HeightSync_CheatingTrail` |
| — | Feed stopped / recovery | ✅ `…_HeightSyncFeedStopped_*` (2) | ✅ `FeedStoppedOmits` / `FeedRecovers` |
| E1 | Courier bootstrap | ✅ `…_CourierBootstrap` | ❌ Phase D |
| E2 | Lazy carry-forward | ✅ `…_LazyCarryForwardOutsideSyncTurn` (`-tags=dev`) | ❌ Phase D |
| E3 | Stale origin rejected | ✅ `…_StaleOriginRejected` (`-tags=dev`) | ❌ Phase D |
| E4–E6 | `(C-quorum)` / mixed heights / stale oracle | ✅ `heightsync_anchor_e2e_confirm_test.go` | ❌ Phase D |
| E7 | Pipelined courier | ✅ `…_PipelinedCourier` | ❌ Phase D |
| E8 | Held originator replay | ✅ `…_HeldOriginatorReplayRejected` (`-tags=dev`, `-short` skip) | ❌ Phase D |
| E9–E10 | Response origin sig + exculpation | ✅ `heightsync_anchor_e2e_origin_sig_test.go` | ❌ Phase D |
| E11 | Late oracle via courier | ✅ `…_LateOracleHost_*` (2) | ❌ Phase D |
| — | Smoke | — | ✅ `TestContainerE2E_HeightSync_Smoke` |
| S1–S12 | Strong mode | ❌ planned | ❌ Phase E |

Forced-sync-turn sub-scenarios **A–D** (full window, re-entry, cadence
coalesce, resume) remain **❌** in both tiers; only **E** (malicious user Omits)
is implemented in-process. See §9.3 item 6b below.

Container: **Phase A–C** implemented (`make e2e`, `make e2e-phase-c`). **Phase D**
= container ports of E1–E11 + forced-turn + carry-forward. **Phase E** = Strong
(S1–S12). Details: [`CONTAINER_E2E_PLAN.md`](CONTAINER_E2E_PLAN.md) §7.

---

## Preparation

All commands assume the **`devshard/`** Go module root unless noted.

### In-process (no Docker)

```bash
cd devshard
go test -count=1 ./testenv/scenarios/...
```

### Container (Docker)

Requirements: **Docker**, **docker compose v2**, **Go**. Build tag **`testenvci`**
is required (`container/` is excluded from plain `go test`).

**Session model (default):** one shared escrow session for the whole `make e2e`
run. Each test calls `advanceSessionToNonce` / `nextSyncTurnLeadNonce` from
`GET /v1/status` — no per-test SQLite wipe. For a fresh session at nonce 1:
`RESET_SESSION=1 make e2e`.

**Recommended:** use the driver script (one shared stack, like citest):

1. Regenerate `config.yaml` / `docker-compose.yml` (unless `SKIP_REGEN=1`)
2. Preflight scheduler env (`K ≥ slots`)
3. `docker compose up --build` once (project **`heightsynce2e`**, obs under **`.container-e2e-obs-data/`**)
4. Wait for height-sync, devshardctl, Loki, VictoriaMetrics
5. `go test` with **`TESTENV_REUSE_STACK=1`** — each test resets host DB + restarts devshardd/ctl, then runs traffic

```bash
# All container height-sync tests (Phase A + B)
bash devshard/testenv/scripts/run-container-heightsync-e2e.sh

# Phase B only
CONTAINER_E2E_PHASE=b bash devshard/testenv/scripts/run-container-heightsync-e2e.sh
```

From `devshard/testenv/`: `make e2e`, `make e2e-phase-b`. Leave stack up after a run: `SKIP_DOWN=1 make e2e`.

**Isolated mode** (each test does its own `compose up` in `t.TempDir()` — slow, no script):

```bash
cd devshard/testenv && make e2e-isolated
# or: cd devshard && go test -tags=testenvci ./testenv/scenarios/container/...
```

Do not run `run-stack-citest.sh` / `make citest-stack` at the same time (same **172.30.0.0/24** subnet and host ports). Skip Docker: `TESTENV_SKIP_DOCKER_STACK=1`.

Cheating-trail on compose needs `devshardctl` with **`DEVSHARDCTL_DEBUG=1`**
(debug route `POST /v1/debug/cheat-anchor`).

**Troubleshooting — `Pool overlaps with other one on this address space`:** every
testenv compose file uses bridge subnet **`172.30.0.0/24`**. Only one such network
can exist on the Docker host at a time. Stop other stacks before container E2E:

```bash
cd devshard/testenv
docker compose down --remove-orphans
docker network ls | grep _testenv   # should be empty (or only unrelated names)
```

Do not run `run-stack-citest.sh` / `make up` and container E2E at the same time.
Container tests call `PruneStaleContainerE2EDockerStacks` before each `compose up`
to tear down leftover `*_testenv` projects (including `testenv_testenv` and
`citest*`).

### `run-stack-citest.sh` — block oracle only

[`run-stack-citest.sh`](../scripts/run-stack-citest.sh) regenerates integration
config, runs preflight, starts the **shared** citest stack, then
`testenv/citest` (I1, observability §7.7, I2a, I2b, I9). Use it for
**block-oracle** rows (see [Block oracle I1–I10](#block-oracle-i1i10)), **not** for
`TestContainerE2E_HeightSync_*`. Height-sync container tests use
**`run-container-heightsync-e2e.sh`** above.

---

## Height-sync scenarios (baseline — legacy §9.3 index)

The sections below retain the original PoC §9.3 numbering for container
rollout traceability. For v2 courier / confirmation / signature scenarios,
see [`height-sync-tests.md`](../../docs/height-sync-tests.md) §4.

### §9.3 items 1–4 — Initial sync turn and cadence

**Status:** In-process ✅ · Container ✅

**What it proves.** With a fresh escrow and a ticking oracle, height-sync
bootstrap and periodic cadence work end-to-end across four round-robin hosts.

1. **Initial sync turn (nonces 1–4).** Every user request and every host’s first
   response in the session carries an **Anchor** (`height_sync` with
   `proof_type = height-anchor-v1`). Each host records one inbound user
   attestation; the user records one host attestation per `peer_id`. Hashes match
   the local oracle for that height (equality only — PoC does not verify mainnet).

2. **Cadence (nonces 5–16).** User outbound modes follow the scheduler:
   `5..7` Omit, `8..11` Anchor, `12..15` Omit, `16` Anchor (nine request Anchors
   total: `{1..4} ∪ {8..11} ∪ {16}`). Each host logs Anchor only on nonces it
   serves via round-robin; others Omit. On every Anchored nonce, the user’s
   `block_hash_prefix` equals the host’s `peer_block_hash_prefix` in logs. Audit
   rings grow across all four peers as later sync turns complete.

**In-process:** `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail` — also embeds
lost-first-response and carry-forward checks in the same file’s broader flow;
this test name is the dedicated cadence + wiring assertion.

**Compose:** `TestContainerE2E_HeightSync_Cadence` — same nonce pattern; asserts
via Loki LogQL and Prometheus counters on the real stack.

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail$'
go test -tags=testenvci -count=1 -timeout=25m -v ./testenv/scenarios/container/... \
  -run '^TestContainerE2E_HeightSync_Cadence$'
```

**Unit support (§9.1–§9.2):** `devshard/heightsync` scheduler tests,
`devshard/transport/envelope_test.go`,
`TestHTTPClient_Send_HeightSync_ProtobufRequestAndAudit`.

---

### §9.3 item 4.1 — Cross-host higher-tip carry-forward

**Status:** In-process ✅ · Container ❌ (planned — heterogeneous `heightsyncd`; [`CONTAINER_E2E_PLAN.md`](CONTAINER_E2E_PLAN.md) §7.5)

**What it proves.** When one host’s oracle is **ahead** (`X+1`) and others (and
the user client) are at `X`, the user **carries** the higher tip learned from that
host into later in-turn Anchors sent to hosts still at `X`. Those hosts **store**
the inbound `(X+1, hash')` in their audit rings even though their own oracle has
not reached `X+1` yet — the same “ahead of local oracle” case labeled
**`untrusted_peer`** in logs and audit trust fields.

**Setup.** One host wired to `(X+1, hash')`; hosts and user oracle at `(X, hash)`.
Drive nonces `1..4` (initial sync turn).

**In-process:** `TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts`

**Compose:** `TestContainerE2E_HeightSync_CarriesHigherPeerTip` — **not implemented**.

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts$'
```

---

### §9.3 item 5 — Lost-first-response self-healing

**Status:** In-process ✅ · Container ✅

**What it proves.** Height-sync bootstrap does **not** require a dedicated
height-sync RPC or restarting a failed host. If the executor for the **sync-turn
lead** (absolute nonces **1**, **8**, **16**, … — periodic leads have
`nonce % K == 0`) never delivers an HTTP response (no SSE / receipt), the
**next** inference (lead+1, next round-robin slot, still in the sync-turn window)
still **Anchors** on the user request. The session recovers through normal
inference traffic only.

**Session behavior (important).** The user nonce advances on
`PrepareInference` (`MsgStartInference` in the diff). The next nonce does
**not** wait for `MsgConfirmStart` from the previous host — Confirm is pipelined
into a later diff. This scenario tests a **lost first HTTP response** from the
lead executor, not “wait for the previous inference to finish.”

**Actions.**

| Layer | Failure injection | Then |
|--------|-------------------|------|
| **In-process** | `PrepareInference` at lead; **close** that host’s HTTP server; `SendOnly` fails (no response body). | `SendInference` at lead+1; assert request Anchor; continue through lead+3; audit ring populated. |
| **Compose** | Advance to sync-turn **lead** from `GET /v1/status`; on the lead’s `devshardd-testenv-{lead%4}`: **`POST /v1/debug/arm-hold-inference-response`** (`DEVSHARDD_DEBUG=1`); **`POST` chat completion** at lead; **`docker compose stop`** that host while the hold blocks SSE (proxy logs `SendOnly failed`). | `compose start` host; inference at **lead+1**; Loki: request **Anchor** at recover nonce. |

Stopping the host **before** the lead `POST` is **not** valid — the session may
already have advanced past the lead during warm-up, or the failure mode is “host
down before send,” not “response lost after the host accepted the diff.”

**In-process:** `TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing` (lead = 1
on a fresh session).

**Compose:** `TestContainerE2E_HeightSync_LostFirstResponse` — hold + POST + stop;
manual repro: `devshard/testenv/scripts/debug-lost-first-response.sh`.

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing$'
go test -tags=testenvci -count=1 -timeout=25m -v ./testenv/scenarios/container/... \
  -run '^TestContainerE2E_HeightSync_LostFirstResponse$'

CONTAINER_E2E_PHASE=b bash devshard/testenv/scripts/run-container-heightsync-e2e.sh
```

---

### §9.3 item 6a — Manual force, single envelope

**Status:** In-process ✅ · Container ✅

**What it proves.** The convenience flag `ForceHeightSyncAnchor` on
`InferenceParams` (and `force_height_sync_anchor` through `devshardctl`) forces
**one** outbound user Anchor on a nonce that cadence would **Omit** — it does
**not** open a full forced sync turn across all slots.

**Flow.** Warm up nonces `1..6` (`5`, `6` Omit on user requests). Nonce **7** is
Omit under cadence. Send nonce **7** with force enabled → exactly **one** extra
user request Anchor; the host serving nonce `7` records **one** additional inbound
user Anchor.

**Limitation.** Other hosts still see Omit on adjacent nonces; the group does not
fully align on `(height, hash)` like a cadence sync turn. Full-window behavior is
§6b below.

**In-process:** `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`

**Compose:** `TestContainerE2E_HeightSync_ForceAnchorSingleMessage` — force flag
must pass through the HTTP proxy JSON body.

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn$'
go test -tags=testenvci -count=1 -timeout=25m -v ./testenv/scenarios/container/... \
  -run '^TestContainerE2E_HeightSync_ForceAnchorSingleMessage$'

go test -count=1 -v ./transport -run 'ForceAnchor_OnInferenceRequest|ForceHeightSyncAnchor_TransportJSONRoundTrip'
go test -count=1 -v ./heightsync -run '^TestAnchorScheduler_ForceAnchorOverridesOmitWindow$'
```

---

### §9.3 item 6b — Forced sync turn (`MsgForceHeightSyncTurn`)

**Status:** Sub-scenarios A–D ❌ (in-process and container). Sub-scenario **E** in-process ✅; container ❌.

**What it proves (target behavior).** A **single diff transaction**
`MsgForceHeightSyncTurn` opens a forced window `[StartNonce, EndNonce]` on
**every** host’s escrow state. Host **responses** in that window **must** Anchor
(normative). User **requests** in the window are best-effort (honest client
Anchors; malicious user may Omit — hosts log
`height_sync_force_request_anchor_missing` as dispute evidence but still process
the inference). Re-entry while active is ignored; overlap with cadence windows
is coalesced (no double-Anchor on boundary nonces).

| Sub-scenario | Intent | In-process test | In-process | Container |
|--------------|--------|-----------------|------------|-----------|
| **A** | Honest user: `+slots_num` request and response Anchors across the window; single force tx in trigger diff only | `TestHeightSyncAnchor_E2E_ForcedSyncTurn_AnchorsEntireSlotWindow` | ❌ | ❌ |
| **B** | Second force while window active → no-op, no extension | `TestHeightSyncAnchor_E2E_ForcedSyncTurn_IgnoresReentryWhileActive` | ❌ | ❌ |
| **C** | Forced window overlaps periodic cadence → cadence swallowed for that period (e.g. nonce 11 Omit when forced ends at 10 inside `{8..11}`) | `TestHeightSyncAnchor_E2E_ForcedSyncTurn_CoalescesWithPlannedCadence` | ❌ | ❌ |
| **D** | Forced turn strictly between cadence windows → later cadence unaffected | `TestHeightSyncAnchor_E2E_ForcedSyncTurn_RestartCadenceAfterClose` | ❌ | ❌ |
| **E** | Malicious user strips request Anchors in window → hosts still Anchor responses; warn entries; zero inbound user-Anchor delta | `TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits` | ✅ | ❌ |

**Compose:** `TestContainerE2E_HeightSync_ForcedSyncTurn_*` — **not implemented** (see [`CONTAINER_E2E_PLAN.md`](CONTAINER_E2E_PLAN.md) §5.5).

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits$'
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_ForcedSyncTurn_'

go test -count=1 -v ./user -run '^TestUser_ForceHeightSyncTurn_AppearsOnlyInTriggerDiff$'
go test -count=1 -v ./transport -run 'TestServer_Inference_HeightSync_ForcedTurn_'
```

---

### §9.3 item 7 — Cheating trail (bogus hash at honest height)

**Status:** In-process ✅ · Container ✅

**What it proves.** The PoC **does not** live-verify Anchors against canonical
mainnet; it **stores what each peer claimed**. A dishonest user can send an Anchor
at the **same** `mainnet_height` as honest oracles but with a **fabricated**
`mainnet_block_hash`. The host must **accept** the inference (no rejection) and
store the **bogus hash verbatim** in the audit ring (`direction=request`,
`Trust=peer_aligned` — same height, hash disagreement not verified on the hot
path). An offline verifier compares the ring to canonical `(H, hash_canonical)` to
detect cheating.

**Mechanism.** In-process: `HeightSyncRequestMutateHook` after scheduler decide,
nonce **1** only. Compose: one-shot `POST /v1/debug/cheat-anchor` on
`devshardctl`.

**In-process:** `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`

**Compose:** `TestContainerE2E_HeightSync_CheatingTrail`

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash$'
go test -tags=testenvci -count=1 -timeout=25m -v ./testenv/scenarios/container/... \
  -run '^TestContainerE2E_HeightSync_CheatingTrail$'
```

---

### §9.3 item 8 — Height-sync feed stopped

**Status:** In-process ✅ (2 tests) · Container ✅ ([`CONTAINER_E2E_PLAN.md`](CONTAINER_E2E_PLAN.md) §7.3 — `make e2e-phase-c`)

**What it proves.** When the block-oracle feed fails (`heightsyncd` stopped in
production; shared stopping oracle in-process), `AnchorScheduler.Decide` degrades
to **Omit** on oracle error — even **inside** an active sync turn. Inferences
still **succeed** (HTTP 200, receipts delivered); no panic or hard failure.

**Two tests.**

1. **Omit without errors.** Nonce 1 Anchors while feed is up; stop feed; nonce 2
   (still in initial `1..4` window) → user and host logs show `mode=omit`; request
   anchor count does not increase.

2. **Recovery.** With feed stopped, nonces `2..7` Omit; restore feed; nonce **8**
   starts periodic window `{8..11}` → `mode=anchor` again.

**In-process:** `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors`,
`TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns`

**Compose:** `TestContainerE2E_HeightSync_FeedStoppedOmits` /
`FeedRecovers` — `docker compose stop|start height-sync`,
`MOCKDAPI_STALE_AFTER=3s`, relative sync-turn leads; **FeedRecovers**
waits for `/block/latest` to advance, then **`compose restart devshardctl`**
only (refreshes devshardctl mockdapi SSE without restarting hosts).

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_'
cd devshard/testenv && make e2e-phase-c
```

---

### §9.4 — Smoke

**Status:** Container ✅ (`make e2e-smoke` or `CONTAINER_E2E_RUN='^TestContainerE2E_HeightSync_Smoke$' make e2e`)

**What it proves.** Minimal gate: bring up the compose stack, drive at least one
inference, and observe **at least one** height-sync Anchor in logs or metrics
(any direction). Thin wrapper around cadence path for CI/shell smoke.

**Compose:** `TestContainerE2E_HeightSync_Smoke` — **implemented**.

---

## §9.3-adjacent unit scenarios

These live outside `testenv/scenarios` but pin trust labels and reconciliation
used by §9.3 item 4.1. See plan §6.2 and §9.1b.

| Test | Package | Status |
|------|---------|--------|
| `TestInboundTrust` | `heightsync` | ✅ |
| `TestServer_Inference_HeightSync_OutboundAnchor` | `transport` | ✅ |
| `TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns` | `transport` | ✅ |
| `TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn` | `transport` | ✅ |
| `TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits` | `transport` | ✅ (supports §9.3 **6b-E**, not a full E2E) |

### Inbound trust mapping — `TestInboundTrust` (`heightsync`)

**Status:** ✅

**What it proves.** `heightsync.InboundTrust` returns **`untrusted_peer`** when
the Anchor height is **strictly above** the host oracle tip, and **`peer_aligned`**
when at or below — no HTTP stack required.

```bash
go test -count=1 -v ./heightsync -run '^TestInboundTrust$'
```

### Outbound Anchor is oracle-trusted — `TestServer_Inference_HeightSync_OutboundAnchor` (`transport`)

**Status:** ✅

**What it proves.** Host SSE receipts carry `height_sync` from the scheduler;
audit **`response`** rows use **`Trust=trusted_oracle`**.

```bash
go test -count=1 -v ./transport -run '^TestServer_Inference_HeightSync_OutboundAnchor$'
```

### Oracle reconciles ahead-of-oracle tip — mismatch — `TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns`

**Status:** ✅

**What it proves.** User sends Anchor at height **H₁** above host oracle **H₀**;
host stores pending `(H₁, hash_peer)`. Oracle later reaches **H₁** with a
**different** hash → exactly **one** Warn:
`untrusted peer tip disagrees with oracle at reconciled height`.

```bash
go test -count=1 -v ./transport -run '^TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns$'
```

### Oracle reconciles — match — `TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn`

**Status:** ✅

**What it proves.** Same setup, but oracle hash **matches** the stored peer tip →
follow-up inference completes **without** that Warn.

```bash
go test -count=1 -v ./transport -run '^TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn$'
```

```bash
go test -count=1 -v ./transport -run 'TestServer_Inference_HeightSync_(OutboundAnchor|UntrustedReconcile)'
```

---

## Block oracle I1–I10

**What it proves.** Phase 3 **multi-validator mock block oracle**: quorum floors,
signer rotation, verifier rejection paths, `heightsyncd` stream vs pinned
validator set (I9), and compose wiring (I1 bootstrap, I2 height spread, §7.7
Grafana/Loki/VM). **Not** the same as height-sync Anchor cadence E2E — different
package (`testenv/citest`).

| Row | Scenario | Compose citest (`testenv/citest`, `-tags=testenvci`) |
|-----|----------|------------------------------------------------------|
| I1 | Bootstrap | ✅ (in `TestStackIntegrationI1andSection8_7`) |
| I2a | Height convergence (protocol) | ✅ |
| I2b | Height convergence (observability) | ✅ |
| I3 | Hostile header rejection | ❌ |
| I4 | Height-sync outage | ❌ |
| I5 | Inference happy path | ❌ |
| I6 | Gossip consistency | ❌ |
| I7 | Settlement | ❌ |
| I8 | Host crash and recovery | ❌ |
| I9 | Multi-validator stream vs. auditor | ✅ |
| I10 | Foreign-signature injection | ❌ |

Unit matrix (`devshard/blockoracle/...`, `testenv/config`): ✅ — see [`blockoracle_phase3_integration.md`](blockoracle_phase3_integration.md) §7.1.1.

Full row table: [`blockoracle_phase3_integration.md`](blockoracle_phase3_integration.md).

```bash
cd devshard/testenv && bash ./scripts/run-stack-citest.sh
go test -count=1 ./blockoracle/...   # from devshard/ — unit matrix §7.1.1
```

---

## Layout / config sanity — `TestScenarios_ConfigPresent`

**Status:** ✅

**What it proves.** `devshard/testenv/config.yaml` parses, satisfies constraints,
and has a non-empty `height_sync.validators` list (required for compose and I9).

```bash
go test -count=1 -v ./testenv/scenarios -run '^TestScenarios_ConfigPresent$'
```

---

## Protocol v2 — courier, confirmation, origin signatures

**Status:** In-process ✅ for all rows; container ❌ until Phase D.

Run the full v2 in-process suite:

```bash
cd devshard
go test -count=1 ./testenv/scenarios/ -run '^TestHeightSyncAnchor_E2E_'
# Held-response / replay variants (E2, E3, E8):
go test -tags=dev -count=1 ./testenv/scenarios/ -run '^TestHeightSyncAnchor_E2E_'
```

| ID | Test | File | What it proves |
|----|------|------|----------------|
| E1 | `…_CourierBootstrap` | `heightsync_anchor_e2e_courier_test.go` | Cold cache ⇒ Omit; responses populate peer tips; carry uses host originator. |
| E7 | `…_PipelinedCourier` | same | Concurrent sync turn; warm cache carries Anchor with originator metadata. |
| E2 | `…_LazyCarryForwardOutsideSyncTurn` | `heightsync_anchor_e2e_test.go` | Lazy propagate outside sync turn; `last_propagated` dedupes. |
| E3 | `…_StaleOriginRejected` | same | `F` freshness ⇒ `stale_origin`. |
| E8 | `…_HeldOriginatorReplayRejected` | same | 70 s hold ⇒ replay rejected (slow; `-short` skips). |
| E4 | `…_IsStrictlyConfirmed_Quorum` | `heightsync_anchor_e2e_confirm_test.go` | `(C-quorum)` at `Q = 3/4`. |
| E5 | `…_MixedHeights_Confirmed` | same | One bad hash cannot un-confirm quorum height. |
| E6 | `…_StaleOracle_Inconclusive` | same | Stale oracle ⇒ `stale` for all heights. |
| E11 | `…_LateOracleHost_*` | same | Courier propagates quorum to late host (2 tests). |
| E9 | `…_ResponseOriginSignatureVerified` | `heightsync_anchor_e2e_origin_sig_test.go` | Host signs response; user verifies + caches blob. |
| E9b | `…_ResponseOriginSignatureInvalidDropped` | same | Tampered sig dropped; metric increments. |
| E10 | `…_CarrierExculpation` | same | `HeightSyncEvidenceFor` returns verified blob; fresh cache cannot exculpate. |

Normative mapping: [`height-sync-tests.md`](../../docs/height-sync-tests.md) §4 and §7–§8.

---

## Planned — Strong mode and follow-ons

**Status:** ⏳ — catalogued in [`height-sync-tests.md`](../../docs/height-sync-tests.md) §6
(S1–S12 in-process, Phase E container). Open design notes:
[`height-sync-open-questions.md`](../../plans/height-sync-open-questions.md).

- **Strong / `D` band** — `LightBlock` + `VerifyCommit`; `(C-strong)` / `(C-hybrid)`.
- **Forced sync turn A–D** — full-window honest user, re-entry, cadence coalesce,
  resume (E is done in-process only).
- **Production-realism / malware suite** — [`CONTAINER_E2E_PLAN.md`](CONTAINER_E2E_PLAN.md) §12
  (per-host drift, colluding cheaters; not the same as baseline container tests).
- **On-chain evidence tx** — deferred to cPoC / dispute plans.

---

## Quick reference

```bash
cd devshard

# All in-process height-sync e2e (baseline + v2)
go test -count=1 -v ./testenv/scenarios/ -run '^TestHeightSyncAnchor_E2E_'
go test -tags=dev -count=1 -v ./testenv/scenarios/ -run '^TestHeightSyncAnchor_E2E_(Lazy|Stale|Held)'

# Container height-sync (baseline only — Phases A–C; see status table)
bash testenv/scripts/run-container-heightsync-e2e.sh

# Block-oracle citest stack (I1, I9, …) — not height-sync container E2E
cd testenv && bash ./scripts/run-stack-citest.sh
```
