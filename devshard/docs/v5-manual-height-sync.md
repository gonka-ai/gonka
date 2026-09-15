# v5 manual tests: height-sync

Operator manual for **block 1** of [`v5-merged-features-test-plan.md`](./v5-merged-features-test-plan.md). Automated catalog: [`height-sync-tests.md`](./height-sync-tests.md). Spec: [`proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md).

Default `D` (`DeltaBlocks`) is **2**. Dispute / Strong slash is **not** in this release. Pass means: chat keeps working, and operators can see lag / future / fabricated-hash on logs and Prometheus.

---

## What it is

User ↔ host envelopes may carry a `(mainnet_height, block_hash)` Anchor. The **gateway is a courier**: it never mints a height from its own chain read. Hosts sign response-leg Anchors; the gateway carries the best host tip to the next host. The same heights are written into the session log (`MsgHeartbeat` / `MsgHeightAck` / stamped inference txs).

Logical time `F` is the high-water mark of **host-signed** claims. A lagging honest host **lifts** to `F` and reports `CATCHING_UP`. Chat must keep flowing at any roster spread.

`DEVSHARD_GATEWAY_CHAIN_ORACLE` defaults **off**. Leave it unset. It only adds a local follower for trust labels and `delta` logs; it must not skip host seed or change outbound stamps.

Lie/lag overlays (`DEVSHARD_TESTENV_ORACLE_HEIGHT_DELTA`, `DEVSHARD_TESTENV_ORACLE_FABRICATE_HASH`) exist only on `devshard_testenv` binaries (`make -C devshard/testenv build-devshardd`). Production builds ignore them.

---

## How to observe

Join gateway is `http://127.0.0.1:18080` unless you remapped it. Admin Bearer is `DEVSHARD_ADMIN_API_KEY`. Set `DEVSHARD_LOG_LEVEL=debug` on `devshardd` / `devshardctl` to see `delta` / `trust_level`. Warn/error lines appear at default info+.

```bash
GATEWAY=http://127.0.0.1:18080
ADMIN_KEY=sk-admin-your-key   # from config.devshard.env

# Chat (must stay 200 / SSE complete; never hang or 5xx because of height-sync)
curl -sS "$GATEWAY/v1/chat/completions" \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"<model>","messages":[{"role":"user","content":"height-sync smoke"}],"max_tokens":32}'

# Metrics
curl -sS "$GATEWAY/metrics" | grep -E 'devshard_gateway_heightsync_(host_height|height_spread|host_height_lag|cadence_events_total)'

# Per-escrow tips / cadence
curl -sS "$GATEWAY/v1/debug/heightsync" -H "Authorization: Bearer $ADMIN_KEY" | jq .
```

| Signal | Where | Meaning |
| ------ | ----- | ------- |
| `heightsync: peer attestation received` `delta` `trust_level` | host + gateway **debug** | Every inbound/outbound Anchor. Negative `delta` = peer below local tip |
| `trust_level=untrusted_peer` | same | Peer height **strictly greater** than local oracle |
| `trust_level=peer_aligned` | same | Peer height **≤** local oracle |
| `heightsync: untrusted peer tip disagrees with oracle at reconciled height` | host **warn** | Held future tip reached real height; hash ≠ canonical |
| `heightsync: logplane` `L0` `INVALID` `height_regression` | **error** | Stamp below `F` after alignment — protocol break, nonce not consumed |
| `heightsync: logplane` `L5a` `MARK` `l5a_admission` | **debug** | `\|H − local_aligned\| > D` at admission |
| `heightsync: logplane` `L6` `DEFERRED_FAIL` | **debug** | Canonical hash at `H` ≠ claimed hash |
| `devshard_gateway_heightsync_height_spread` | gateway `/metrics` | Max − min fresh (and stale) host claims |
| `devshard_gateway_heightsync_host_height_lag` | gateway `/metrics` | Leader − this slot |
| `devshard_heightsync_marks_total{kind=…}` | host/gateway | `l5a_admission`, `deferred_fail`, `height_unbacked`, … |

Reproducible A/B/C (one dishonest oracle on the solo identity):

```bash
make -C devshard/testenv build-devshardd citest-height-sync
```

---

## Happy path — honest roster

```gherkin
Feature: Height-sync on an honest stack
  Scenario: First chat seeds and chat keeps working
    Given a v5 gateway and hosts with DEVSHARD_GATEWAY_CHAIN_ORACLE unset
    When a chat completion is sent
    Then the response is HTTP 200 (JSON or SSE ending in [DONE])
    And logs contain heightsync: emit mode=anchor
    And GET /v1/debug/heightsync lists the live escrow
    And every slot has a tip
    And sync_state is SYNCED or empty until the first heartbeat
    And height_spread is small
    And join compose does not set DEVSHARD_GATEWAY_CHAIN_ORACLE
```

**Do this**

1. Confirm join / compose does **not** set `DEVSHARD_GATEWAY_CHAIN_ORACLE`.
2. Send one chat (command above).
3. Grep host and gateway logs for `heightsync: emit` `mode=anchor`.
4. Scrape `/metrics`: `host_height` present; `height_spread` small on an honest roster.
5. `GET /v1/debug/heightsync`: escrow listed; each slot has a tip.

**Pass:** chat 200; seed came from **hosts**, not a local gateway tip; spread is small.

---

## Scenario A — host reports a **lower** height than the roster

Honest lag, including *much* lower (`≫ D`): `F` is the max host-signed claim. The lagging host **lifts** to `F` and reports `CATCHING_UP`. Inferences must not stall. A response that stamps **below** `F` after the host already saw the aligned floor is not honest lag — that is `INVALID(height_regression)` (diff rejected, nonce not consumed).

```gherkin
Scenario: Host reports a lower height than the roster
  Given an escrow with honest hosts at height H
  And one host whose oracle tip is much lower than H
  When the gateway has already aligned on the higher tip
  And chat is sent to the lagging host
  Then inferences complete without error
  And the floor F is the higher host-signed height
  And the lagging host lifts to F in the log and reports CATCHING_UP
  And operators see negative delta, height_spread, and host_height_lag
  And a Diff stamp below F after alignment is INVALID(height_regression)
```

**Do this**

- Reproducible: `make -C devshard/testenv citest-height-sync` (overlay Δ=`-20` on the solo identity). Expect chat 200, `height_spread` ≥ 15, negative `delta`.
- Live stack: if one host is behind, or stop one host so its claim goes stale:

```text
msg="heightsync: peer attestation received"  delta=<negative>  trust_level=peer_aligned
msg="heightsync: logplane" check=L0 verdict=INVALID reason=height_regression   # only if a host understates F

devshard_gateway_heightsync_host_height_lag{slot="<lagging>"} > 0
devshard_gateway_heightsync_height_spread  == that lag (or larger)

POST /v1/chat/completions  →  200
```

Stamp-below-`F` after alignment is **unit-only** (citest cannot inject a non-lifting producer). Optional: `go test ./devshard/heightsync/ -run 'TestLogPlane_AckBelowFloor|TestLogPlane_RefStampBelowFloor' -count=1 -v`

**Pass:** chat keeps working; higher `F` wins; lagging slot is visible in spread/lag; a stamp below `F` after alignment is an **error** logplane line, not a silent accept.

---

## Scenario B — future height, unknown hash, `|Δ| > D`

A host claims `H_local + Δ` with `Δ > D` (default `D = 2`) and a hash nobody can look up yet. Chat still serves. Log-plane L5a records `MARK(l5a_admission)` — it does **not** `INVALID` the diff. Strong slash is not shipped.

```gherkin
Scenario: Host reports a future height with unknown hash beyond D
  Given D is 2
  And one host claims H+Δ with Δ > D and a hash not in the honest oracle
  When chat continues
  Then chat still returns 200
  And trust_level is untrusted_peer
  And L5a records MARK(l5a_admission) when that height is bound on a heartbeat or ack
  And Strong slash is not required in this release
```

**Do this**

- Reproducible: `citest-height-sync` (solo Δ=`+10` + fabricated hash).
- Live: you will not see this on an honest host. If you do (or on a stub oracle `Latest() = H+100`):

```text
msg="heightsync: peer attestation received"  delta>2  trust_level=untrusted_peer
msg="heightsync: logplane" check=L5a verdict=MARK reason=l5a_admission

devshard_heightsync_marks_total{kind="l5a_admission"}  increases
devshard_gateway_heightsync_height_spread              ≥ that Δ (if the claim is fresh)
```

**Pass:** no session crash; L5a mark + counter; spread/lag show the outlier; **no** Strong slash.

---

## Scenario C — slightly future (`|Δ| ≤ D`) **fabricated** hash

The pair is admitted (inside `D`). When honest followers reach that height and see the canonical hash: host **warn**, log-plane L6 `MARK(deferred_fail)`, audit ring stores the bogus hash. Chat was never blocked. Slash is deferred.

```gherkin
Scenario: Host reports a slightly future fabricated hash
  Given one host claims H+1 (Δ ≤ D) with a fabricated block hash
  When honest followers later reach height H+1 and see the canonical hash
  Then hosts log warn "heightsync: untrusted peer tip disagrees with oracle at reconciled height"
  And L6 DEFERRED_FAIL is recorded when Oracle.At(H) is available
  And chat was never blocked
```

**Do this**

- Reproducible: `citest-height-sync` (solo Δ=`+1` + fabricated hash). Wait until the honest oracle crosses the claimed `H`.
- Live: after chain tip crosses a bad claimed `H`:

```text
msg="heightsync: untrusted peer tip disagrees with oracle at reconciled height"   # WARN
msg="heightsync: logplane" check=L6 verdict=DEFERRED_FAIL

devshard_heightsync_marks_total{kind="deferred_fail"}  increases
```

**Pass:** chat worked the whole time; when real `H` arrives, warn + L6 mark; no slash.

---

## Cadence, seed, floor

Not the three lie scenarios; still this feature.

```gherkin
Scenario: Quiet escrow heartbeats
  Given two chats have landed host-signed confirm/finish (F exists)
  When Interval (6s) passes with no further chat
  Then cadence_events_total{event="heartbeat_opened"} increases

Scenario: Busy escrow discharges by inference
  Given an escrow receiving chat in a loop
  Then heartbeat_opened stays 0

Scenario: Floor survives snapshot restore
  Given a live session
  When one versiond restarts mid-session
  Then the next chat is 200
  And logs do not contain ErrFloorNotRestored
```

**Do this**

1. Two chats seed `F` (chat 1's start is hashless; confirm/finish ride chat 2). Then wait ~6s with **no further chat** → `heartbeat_opened` increments. A third chat in that wait substitutes `discharged_by_inference`. Seed / one chat is not enough; stretching the wait does not help a floorless session.
2. Chat in a loop → `heartbeat_opened` stays 0.
3. Restart one `versiond` mid-session → next chat 200; no `ErrFloorNotRestored`.

Optional: `docker compose pause mock-dapi` then unpause (testenv only) — Omit/degraded then recover.

**Pass:** quiet escrow heartbeats; busy escrow does not; restart does not drop the floor or 5xx chat.
