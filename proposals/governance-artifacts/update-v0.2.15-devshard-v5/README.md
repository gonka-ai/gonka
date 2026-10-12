# Aggregated v5: Height-sync protocol + general devshard fixes

Height-sync write-up, plus the feature-block index of everything else that has merged onto `devshard-0.2.15-v5`.

## Main features

What this line is for, in three pieces. Detail for height-sync is still in [Height-sync protocol (v5)](#height-sync-protocol-v5); Gateway and HA overlay land in files from Danya and Stas.

### 1. Height-sync

A signed, replayable view of mainnet height lives in the escrow log even when there is no inference traffic. Original discussion: [Discussion #1340](https://github.com/gonka-ai/gonka/discussions/1340).

It is the important component that is blocker on devshard evolution and improvement way. Devshard will know cryptographically provable current mainnet height. And can use this height in devshard protocol to legally skip work for participants during cPoC and use this height for timeout and in future protocol improvements.

Height-binded blocktime is logical time that is the clock later releases can use for **protocol-level QoS**: count each participant's token-output throughput, and measure silence in the response token stream. Hosts that stall or under-deliver then stop getting work and can be slashed, so end-user latency tracks the honest majority rather than the slowest dishonest slot.

This PR **carries** the stamps. It does not fold them into `timeout.go` / the seal clock, does not score throughput or slash on silence yet, and does not ship Strong / `LightBlock` disputes.

### 2. Gateway always-stream and better QoS

The gateway always asks the host (and therefore vLLM) for a streamed completion. This is important as gateway now can detect silent windows in stream and measure token output throughput and time to first token.

Because every attempt is a real stream, first-token timeout (`FirstTokenTimeoutFloorMS` + per-input lag), inter-chunk stall, empty/error-stream quarantine, and fail-closed escalation (`escalation_fail_closed` when no winner) apply to JSON clients as well as SSE clients. TTFT lands in `perf_host_samples` for 100% of chat.

Related QoS / shape work on the same line:

- Height-seed loop before the first chat (`proxy.waitHeightSeed`); a miss is `height_seed_incomplete` / HTTP 503, not a hang.
- `n` forced to 1 when present (`ForceLiteralParameter{Value: 1, OverwriteOnly: true}`) so reservation/settlement budget one `MaxTokens` output.
- Logprobs / top_logprobs only if the client asked (`clientResponseIntent`); otherwise stripped at the fold.
- Payload files may compress at rest (`DEVSHARD_PAYLOAD_ZSTD_ENABLED`).
- Gateway accounting (`DEVSHARD_STATS_ENABLED`, epoch-scoped `accounting.Tracker`) and serving a delivered answer whose nonce never closed.

### 3. High-availability and fault-tolerance

Hosts can add, replace, or remove nodes on different machines without downtime. The monolith is split into replicatable parts (router, versiond, child, Postgres); any replicated part can fail or shut down while the host keeps serving. That is the fault-tolerance contract and the base for an upcoming Kubernetes deploy.

Most of the HA work on this line is that lifecycle:

- **Add.** A new versiond on another machine is listed in DNS or the endpoint file. HAProxy takes it only after `/readyz` 200 (`init-state fully-down` until the first successful check). Governance `/versions` projects new protocol names into the router fleet with no host-side edit. Shared Postgres is fail-closed (no SQLite fallback; storage-identity write proofs) so two hosts cannot split history.
- **Replace.** Same-name SHA swap is blue/green: start the new child on a new port, wait `/ready` 200 **and** `recovery_complete`, then swap routes. The old generation drains proxy leases and `POST /drain` before SIGTERM. At most one draining predecessor per version, so rapid catalog updates cannot stack generations and exhaust Postgres. Fleet slots keep the previous generation as a stopped container — a failed commit is `docker start` of exactly what served before. The host updater restores public ingress if admission fails. TLS publish is atomic (never a truncated PEM).
- **Remove.** `docker compose stop` moves versiond to **announcing**: still accepting, already unready (`VERSIOND_DRAIN_ANNOUNCE`). The router withdraws it before it stops taking work; in-flight SSE stays on that generation. Catalog deletions take effect at fleet commit. A membership change drains the old fleet, then admits the new list, so every live slot agrees on placement.

---

## Feature-block index

What landed on this branch after the v4 line, grouped by feature.


| #   | Block                                                                                                                       | Headline                                                                                     | PRs                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| --- | --------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | [Height-sync protocol](devshard/docs/v5-merged-features-test-plan.md#1-height-sync-protocol)                                | Courier alignment, log-plane stamps, heartbeats, detection of lag / future / fabricated hash | [#1615](https://github.com/gonka-ai/gonka/pull/1615), [#1625](https://github.com/gonka-ai/gonka/pull/1625), [#1621](https://github.com/gonka-ai/gonka/pull/1621), [#1649](https://github.com/gonka-ai/gonka/pull/1649), [#1669](https://github.com/gonka-ai/gonka/pull/1669), [#1697](https://github.com/gonka-ai/gonka/pull/1697)                                                                                                                                                                   |
| 2   | [HA overlay: router, catalog, Postgres](devshard/docs/v5-merged-features-test-plan.md#2-ha-overlay-router-catalog-postgres) | HAProxy L7, governance catalog, fail-closed HA storage, live PG readiness                    | [#1599](https://github.com/gonka-ai/gonka/pull/1599), [#1601](https://github.com/gonka-ai/gonka/pull/1601), [#1602](https://github.com/gonka-ai/gonka/pull/1602), [#1655](https://github.com/gonka-ai/gonka/pull/1655), [#1603](https://github.com/gonka-ai/gonka/pull/1603), [#1606](https://github.com/gonka-ai/gonka/pull/1606), [#1604](https://github.com/gonka-ai/gonka/pull/1604), [#1607](https://github.com/gonka-ai/gonka/pull/1607), [#1642](https://github.com/gonka-ai/gonka/pull/1642) |
| 3   | [Warm cutover and session recovery](devshard/docs/v5-merged-features-test-plan.md#3-warm-cutover-and-session-recovery)      | `/ready` split, overlap waits `recovery_complete`, snapshot restore, epoch prune             | [#1653](https://github.com/gonka-ai/gonka/pull/1653), [#1712](https://github.com/gonka-ai/gonka/pull/1712), in-tree warm-cutover, local `ak/devshardd-epoch-evict`                                                                                                                                                                                                                                                                                                                                   |
| 4   | [Gateway v4 → v5](devshard/docs/v5-merged-features-test-plan.md#4-gateway-v4--v5)                                           | Always-stream aggregation, height-seed loop, epoch stats                                     | [#1681](https://github.com/gonka-ai/gonka/pull/1681), [#1684](https://github.com/gonka-ai/gonka/pull/1684)                                                                                                                                                                                                                                                                                                                                                                                           |
| 5   | [Host ping observability](devshard/docs/v5-merged-features-test-plan.md#5-host-ping-observability)                          | Gateway `/clock` pings and dapi mlnode pings; no routing effect                              | [#1580](https://github.com/gonka-ai/gonka/pull/1580)                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 6   | [Validation, misses, settlement](devshard/docs/v5-merged-features-test-plan.md#6-validation-misses-settlement)              | Payload withhold → vote false; error-stream miss; pending drain credit                       | [#1647](https://github.com/gonka-ai/gonka/pull/1647), [#1651](https://github.com/gonka-ai/gonka/pull/1651), [#1652](https://github.com/gonka-ai/gonka/pull/1652), [#1626](https://github.com/gonka-ai/gonka/pull/1626), [#1634](https://github.com/gonka-ai/gonka/pull/1634), [#1512](https://github.com/gonka-ai/gonka/pull/1512)                                                                                                                                                                   |
| 7   | [Security and admission](devshard/docs/v5-merged-features-test-plan.md#7-security-and-admission)                            | Host cannot inject user txs; settled bind; SSRF; canonical escrow ids                        | [#1664](https://github.com/gonka-ai/gonka/pull/1664), [#1627](https://github.com/gonka-ai/gonka/pull/1627), [#1629](https://github.com/gonka-ai/gonka/pull/1629), [#1578](https://github.com/gonka-ai/gonka/pull/1578)                                                                                                                                                                                                                                                                               |
| 8   | [Payload size and chat shape](devshard/docs/v5-merged-features-test-plan.md#8-payload-size-and-chat-shape)                  | Compress at rest; `n=1`; logprobs only if asked; stream parse hardening                      | [#1650](https://github.com/gonka-ai/gonka/pull/1650), [#1676](https://github.com/gonka-ai/gonka/pull/1676), [#1686](https://github.com/gonka-ai/gonka/pull/1686), [#1713](https://github.com/gonka-ai/gonka/pull/1713)                                                                                                                                                                                                                                                                               |
| 9   | [Proxy and version stamp](devshard/docs/v5-merged-features-test-plan.md#9-proxy-and-version-stamp)                          | Atomic TLS install; strip `v` prefix on rotation protocol                                    | [#1661](https://github.com/gonka-ai/gonka/pull/1661), [#1662](https://github.com/gonka-ai/gonka/pull/1662)                                                                                                                                                                                                                                                                                                                                                                                           |
| 10  | [Testenv E2E on v5](devshard/docs/v5-merged-features-test-plan.md#10-testenv-e2e-on-v5)                                     | Re-apply Docker citest to this protocol line                                                 | [#1564](https://github.com/gonka-ai/gonka/pull/1564)                                                                                                                                                                                                                                                                                                                                                                                                                                                 |


Testing manuals in this tree: height-sync → `[v5-manual-height-sync.md](devshard/docs/v5-manual-height-sync.md)`; residual 3 / 5 / 9 → `[v5-manual-residual.md](devshard/docs/v5-manual-residual.md)`. HA overlay (2) and gateway (4, 8) land in files from Stas and Danya.

The height-sync section below is the original protocol landing. Operator metrics ([#1621](https://github.com/gonka-ai/gonka/pull/1621)) and the other blocks in the index have since merged onto this branch.

---

## Proposed Bounties

On-chain payouts from the community-sale contract as IBC USDT (`withdraw_ibc`, same pattern as [proposal #76](https://gonka.gg/network/proposals/76)). Total: **91,300 USDT**.


| GitHub                                             | USDT       | Explanation                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Address                                        |
| -------------------------------------------------- | ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------- |
| [@akup](https://github.com/akup)                   | 36,000     | devshard v4.0.1, v4.0.2, v4.0.3, v4.1.0, v5 + height-sync                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56` |
| [@aikuznetsov](https://github.com/aikuznetsov)     | 6,500      | testing, PR [#1508](https://github.com/gonka-ai/gonka/pull/1508), [#1547](https://github.com/gonka-ai/gonka/pull/1547), [#1564](https://github.com/gonka-ai/gonka/pull/1564), [#1562](https://github.com/gonka-ai/gonka/pull/1562), [#1631](https://github.com/gonka-ai/gonka/pull/1631), [#1649](https://github.com/gonka-ai/gonka/pull/1649), [#1651](https://github.com/gonka-ai/gonka/pull/1651), [#1653](https://github.com/gonka-ai/gonka/pull/1653), [#1555](https://github.com/gonka-ai/gonka/pull/1555)/[#1571](https://github.com/gonka-ai/gonka/pull/1571)                                                                                                                                                                                                                                                                                                                                               | `gonka1frlfyz2wtltdy47dq3w9pwc8ruvjvlthp2lh53` |
| [@snevolin](https://github.com/snevolin)           | 12,000     | high-availability: [setup instruction](https://github.com/gonka-ai/gonka/blob/devshard-0.2.15-v5/docs/devshard-host-ha-setup.md), [test plan](https://github.com/gonka-ai/gonka/blob/devshard-0.2.15-v5/devshard/docs/devshard-host-ha-test-plan.md); HAProxy versiond router, catalog routes, independent fleet (#1599, #1606, #1610); fail-closed Postgres HA, live /readyz, persistent PGDATA, storage proofs (#1642, #1601, #1602, #1603, #1607, #1730); versiond/edge-api drain (#1604, #1605); public HAProxy + crash-safe TLS (#1609, #1661); update-devshard.sh safe migrate to HA layout (#1611)                                                                                                                                                                                                                                                                                                           | `gonka1vnupswg7qz2w5k5ax6zrp02mxmln6arnvjc87h` |
| [@qdanik](https://github.com/qdanik)               | 23,000     | Validation and H1 report fixes: token floor, logprob distance, n>1 ([#1391](https://github.com/gonka-ai/gonka/pull/1391), [#1436](https://github.com/gonka-ai/gonka/pull/1436), [#1676](https://github.com/gonka-ai/gonka/pull/1676)), Payload compression and logprobs gating ([#1650](https://github.com/gonka-ai/gonka/pull/1650), [#1686](https://github.com/gonka-ai/gonka/pull/1686)), Gateway accounting, delivered-answer serving, pre-floor diff replay ([#1581](https://github.com/gonka-ai/gonka/pull/1581), [#1682](https://github.com/gonka-ai/gonka/pull/1682), [#1595](https://github.com/gonka-ai/gonka/pull/1595)), Kimi, DeepSeek and stop_reason fixes ([#1563](https://github.com/gonka-ai/gonka/pull/1563), [#1613](https://github.com/gonka-ai/gonka/pull/1613), [#1713](https://github.com/gonka-ai/gonka/pull/1713)), Gateway v4 release, review of the previous release, H1 report reviews | `gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8` |
| [@redstartechno](https://github.com/redstartechno) | 300        | PR [#1336](https://github.com/gonka-ai/gonka/pull/1336), [#1512](https://github.com/gonka-ai/gonka/pull/1512), [#1460](https://github.com/gonka-ai/gonka/pull/1460)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | `gonka105ce4495mj0mwkxqeasgdzqfq5jjrfq32eza5l` |
| [@shd](https://github.com/shd)                     | 11,500     | devshards protocol review and formalization, [Discussion #1340](https://github.com/gonka-ai/gonka/discussions/1340), includes devshard v4                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `gonka1p2hjjf63dqhpf5qmyaaq6u73zrzsrf3ra3qxk2` |
| [@Ryanchen911](https://github.com/Ryanchen911)     | 2,000      | PR [#1491](https://github.com/gonka-ai/gonka/pull/1491), Issue [#1470](https://github.com/gonka-ai/gonka/issues/1470)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | `gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57` |
| **Total**                                          | **91,300** |                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |                                                |


---

## Height-sync protocol (v5)

**Discussion:** `devshard` [Height-sync protocol](https://github.com/gonka-ai/gonka/discussions/1340)  
**Spec:** `[HEIGHT_SYNC_PROTOCOL_PROPOSAL.md](devshard/docs/proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md)`  
**Plan:** `[height-sync-implementation-plan.md](devshard/docs/height-sync-implementation-plan.md)`  
**Params:** `[height-sync-params.md](devshard/docs/height-sync-params.md)`  
**Tests:** `[height-sync-tests.md](devshard/docs/height-sync-tests.md)`

Sibling (not this PR): `ak/height-sync-protocol-dapi` mounts hash-only `/block/*` on production dapi.

---

## Summary

Original discussion: [Discussion #1340](https://github.com/gonka-ai/gonka/discussions/1340).

This PR lands height-sync on 0.2.15 / v5: every host and the sequencer keep a signed, replayable view of mainnet height inside the escrow log, even when there is no inference traffic.

It has two planes, plus a new kind of time in the log:

1. **Transport plane (Phases A–D).** Anchor envelopes on a `K`-nonce cadence, forced turns, courier peer tips, origin signatures, and `(C-quorum)`. Rewired onto `devshard/chainoracle/blocks` instead of cherry-picking `devshard-testenv`. Default compose stays off; opt in with `DEVSHARD_CHAINORACLE_URL` or `DEVSHARD_HEIGHTSYNC`.
2. **Log plane (Phase E, E0–E7 + E9).** Heartbeat turns, host acks, L0–L5a / L7 ingest checks, repair probes that never assign blame, close-ready arming, and `observed_height` stamps on inference txs so a busy escrow never heartbeats.
3. **Logical time.** Every Diff-resident height — heartbeat, ack, and inference `start` / `confirm` / `finish` — is a *reference height*: `max(own_tip, F(m))`. `F` is the escrow's logical clock, a function of the applied log. Later releases can fold timeouts, `USER_TIMEOUT`, and cPoC bands onto those heights instead of host/user wall-clock timestamps. The same clock is what later **protocol-level QoS** needs: count each participant's token-output throughput (`tokens / Δh` over a stamped stream), and measure silence in the response token stream (gap in `F` between chunks). Hosts that stall or under-deliver then stop getting work and can be slashed, which keeps end-user experience stable. This PR **carries** the stamps; it does **not** switch those decisions yet.

**Deferred to later releases (Phase F / dispute layer):**

- **Strong /** `LightBlock` **proving.** Envelope field 9, `D`-band escalation (`|Δ| > D` ⇒ Strong required), `(C-strong)` / `(C-hybrid)`, and `CATCHING_UP` forcing the next heartbeat to Strong. Until then `CATCHING_UP` is a label only.
- **On-chain disputes.** Marks (`DISPUTE_ORIGINATOR` / `DISPUTE_CARRIER`, vector contradiction, `DEFERRED_FAIL`) are recorded locally. Evidence packets, `MsgHeightSyncEvidence`, slashing, and cross-session equivocation wait on Strong, because the canonical half of a packet is a `LightBlock`.
- **L6 adjudication.** L6 reconciles a Diff-resident `(height, hash)` against the verifier's own oracle. That check runs *after* the pair is already in `Diff`, so it must not `INVALID` the diff (two verifiers whose followers have not reached `H` would split). The implementation records a `DEFERRED_FAIL` mark when the oracle already has block `H` and the hash mismatches. A durable recheck queue for heights that have not mined yet, and turning those marks into disputes, ship with the dispute layer. `F` is not unwound if a later L6 fails.

Also not in the original height-sync landing: cPoC skip carriers, flipping `timeout.go` / the seal clock onto heights, and default-on compose. Gateway operator metrics (plan §8.12, E8) later landed as [#1621](https://github.com/gonka-ai/gonka/pull/1621) on this same branch.

---

## Why

cPoC bands and `USER_TIMEOUT` finalization both need a height that every honest verifier recomputes from `Diff`. Envelope Anchors only exist on the request/response that carried them; a courier user has no follower of its own; a quiet escrow never syncs.

Height-sync puts a signed `(height, hash)` into the log on a wall-clock cadence. Hosts answer. The record of who answered is a pure function of `Diff`. Hosts that hear nothing from the user arm close-ready locally and still emit nothing — closing stays finalization's job.

The schedule is in **milliseconds**, not blocks. Mainnet height is the *result* of a height-sync turnover, so a block-denominated heartbeat is circular: a quiet courier cannot notice a block passed until it syncs, and a partitioned host sees a frozen tip and would never count enough blocks to arm.

---

## Height stamps and logical time

This is the load-bearing new feature. The rest of the protocol landing exists so these stamps keep moving when inference does not.

### Two planes, one meaning in the log


| Plane         | Where                                                          | Height means                                      | Who signs                                                                                                           |
| ------------- | -------------------------------------------------------------- | ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| **Transport** | HeightSyncSection on the HTTP envelope                         | first-party follower tip of *this hop*            | origin signature on the response; request leg unsigned                                                              |
| **Log**       | `observed_height` / `observed_block_hash` on Diff-resident txs | **reference height** — logical time of the escrow | user (diff sig) on heartbeat / start; host (`host_sig` / `executor_sig` / `proposer_sig`) on ack / confirm / finish |


The envelope never enters `Diff`. Two verifiers replaying the journal would not see it, so it cannot be the clock for cPoC or `USER_TIMEOUT`. The stamp in the tx can.

There is **no second height in the log**. A lagging host does not write its raw tip into Diff: it writes `max(own_tip, F(m))`, or omits when the floor is more than `W_conf` above its tip. Its real reading stays first-party in the response-leg Anchor and in `sync_state`.

### What a stamp is

`observed_height` on a message means “the height its signer observed when producing this message” — exactly what `started_at_height` / `confirmed_at_height` would mean on the wire, so those names live only on the **record**, copied from the stamps of `MsgStartInference` and `MsgConfirmStart`.

A stamp is only as strong as the key on it:


| Stamp                                                                                                                                  | Signer                | Strength                                                             |
| -------------------------------------------------------------------------------------------------------------------------------------- | --------------------- | -------------------------------------------------------------------- |
| MsgHeartbeat, MsgStartInference                                                                                                        | user (diff signature) | a **claim** — attributable, checkable against the chain later via L6 |
| MsgHeightAck (`host_sig`), MsgFinishInference (`proposer_sig`), MsgConfirmStart (`executor_sig`, mirrored into ExecutorReceiptContent) | host                  | an **attestation** — rewriting it is forgery of that host            |


The confirm trap: `executor_sig` is over `ExecutorReceiptContent`, a signing input that never enters Diff. A field on `MsgConfirmStart` that is missing from that content is outside the executor's signature, and the sequencer could rewrite the height. This PR mirrors `observed_height` / `observed_block_hash` into the receipt and copies them back before recovery.

Unstamped records omit proto3 zeros, so `post_state_root` bytes do not change (`v2` root).

### `F` is the escrow's logical clock

`F(m)` is the reference height the log had established at nonces `< m`. L0 holds every Diff-resident stamp to it. It never lowers.

Raise rules (spec §14), landed:

1. An envelope `(H, hash)` cannot be a future height (hash unknown in advance). Catch/punish is L6 / Phase F.
2. `F` never exceeds the max reported first-party **host** envelope `H`. Lifts (`stamp = F`) do not raise. Honest compose drops a host raise that does not match this hop's response-leg envelope.
3. Sequencer-composed stamps (`MsgHeartbeat`, `MsgStartInference`) never raise `F` and never count toward quorum `Q`. `Observe` is host-only.

A single party claiming `1<<40` therefore cannot become the escrow's time: the floor only follows host-signed first-party tips, unaided by at most `W_conf`, or a host-only `Q` for a larger jump.

### What this enables, and what it does not switch yet


| Decision today                                     | Height form (later)                                      |
| -------------------------------------------------- | -------------------------------------------------------- |
| Execution timeout against each host's clock        | `h − confirmed_at_height`                                |
| Refusal timeout against user-controlled started_at | `h − started_at_height` (checkable)                      |
| Seal clock = max confirmed_at                      | max `confirmed_at_height`                                |
| Token-output throughput per participant            | `tokens / Δh` over a stamped stream                      |
| Silence in the response token stream               | gap in `F` between consecutive token-bearing stamps      |
| Slow / silent participant                          | stop assigning work; slash once the dispute layer exists |


Migration is two-step on purpose: this PR **carries** the heights; folding them into a state root (timeouts, seals) needs a protocol-version gate, or two participants compute different roots. `timeout.go` / `stateClockLocked` stay on wall time.

---

## What landed


| Phase     | What                                                                                                                                                                                                                                                                                   |
| --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A**     | `devshard/heightsync` transport: Omit/Anchor cadence, inbound classify, confirmation, origin signing, audit. Envelope + `MsgForceHeightSyncTurn` (oneof 9). Host/user/state seams on `chainoracle/blocks`.                                                                             |
| **B**     | In-process e2e in `testenv/scenarios/heightsync_anchor_e2e_*.go`.                                                                                                                                                                                                                      |
| **C**     | Container `citest-height-sync` against mock-dapi `/block/`*. Default compose unchanged; the citest harness patches `DEVSHARD_CHAINORACLE_URL`.                                                                                                                                         |
| **D**     | Hash-only Tendermint observer, direct-chain adapter, host failover when dapi has no `/block/`* or is down. Production dapi mount is the sibling PR.                                                                                                                                    |
| **E0–E3** | `MsgHeartbeat` / `MsgHeightAck` (oneof 10/11; 12/13 reserved for cPoC). User `MaybeHeartbeat` spans, concurrent and non-aborting. Host-signed acks into the mempool from the same oracle read as the response-leg Anchor. Gateway starts the heartbeat loop.                           |
| **E9**    | Session-open seed: fan `POST /sessions/:id/height-sync` once so nonce 1 already has a tip. Consumes no nonce, does not advance `h_last`, never fails the session on a miss.                                                                                                            |
| **E4**    | `ValidateDiff` L0–L5a / L7. Envelope mismatches are marks, not INVALID. Historical replay without an envelope skips L4/L5a. L4 on both legs uses the producer rule (`max(anchor, F)`), not strict equality — a lagging honest sequencer lifting to the floor is not a dispute carrier. |
| **E5**    | Signed host→host repair probe for missing acks. Outcomes `HEIGHT` / `UNREACHABLE` only — never `USER_CHEATING`, never a mark.                                                                                                                                                          |
| **E6**    | Close-ready: host arms after `IdleTimeout` of wall-clock silence and emits nothing.                                                                                                                                                                                                    |
| **E7**    | `observed_height` / `observed_block_hash` on Start / Confirm / Finish. Confirm stamps are mirrored in `ExecutorReceiptContent`. Unstamped records keep the v2 root.                                                                                                                    |


Post-landing audit (steps 12–24 in `height-sync-audit-findings.md`): heartbeat loop on the gateway, fail-closed origin cache, bounded open-turn retain, no local-oracle fold into the SM tracker, compose through the log plane, capped marks, recovered `turn_seq`, host-only floor raise, oracle I/O off mutexes, repair budget/cancel, wire caps, saturating `HReq+D_ack`, `peer_seen` from host claims only.

### Cadence (the E6–E7 rewrite)

Scheduling is wall clock; evaluation stays in blocks so replay never consults a clock.


| Knob                        | Default                                | Job                                                                                                                                                             |
| --------------------------- | -------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Interval                    | 12s                                    | Longest gap between full turnovers                                                                                                                              |
| TurnTimeout                 | 24s (`2 · Interval`)                   | Patience on one open turn; one dead slot cannot silence the cadence                                                                                             |
| IdleTimeout (`T_idle`)      | 48s (`4 · Interval`)                   | Host silence budget before arming                                                                                                                               |
| AckDeadlineBlocks (`D_ack`) | **derived**                            | ⌈(Interval + TurnTimeout) / BlockTime⌉ + 1. Stamp slack; lateness is `observed_height > h_req + D_ack`. `D_ack = 1` made honest span acks late by construction. |
| DeltaBlocks (`D`)           | 2                                      | SYNCED vs CATCHING_UP; Strong escalation is Phase F                                                                                                             |
| WindowBlocks (`W_conf`)     | 256                                    | Confirmation window, unaided floor raise, and how far a producer will carry F                                                                                   |


A **turnover** is `Q` distinct **host-signed** height claims: `MsgHeightAck`, or an executor stamp on ordinary inference. The user's own stamp is self-signed and does not count. `ORACLE_UNAVAILABLE` acks are **required**, echo `F(m)` from the log, and **count** toward `Q` (reachability, not a height witness). Busy inference therefore emits **zero** heartbeats (H2); a quiet session heartbeats every `Interval` (H1). Compose seed does not raise `F`; host-signed confirm/finish of the first inference land on the **next** nonce, so a quiet-heartbeat citest needs two chats before `Interval`.

`K_hb` and `MinRoundsPerBlock` are gone. Snapshot overlay: proto fields 13/15 kept their numbers and became `height_sync_interval_ms` / `height_sync_idle_timeout_ms`; field 18 is `height_sync_turn_timeout_ms`. Inference-chain still publishes zeros, so compiled defaults apply. Overlay scheduling knobs are validated and clamped; evaluation knobs (`D_ack`, `D`, `W_conf`, `BlockTime`) stay compiled.

---

## Wire

`DevshardTx` oneof:


| Field | Message                                                 |
| ----- | ------------------------------------------------------- |
| 9     | `MsgForceHeightSyncTurn` (transport; already allocated) |
| 10    | `MsgHeartbeat`                                          |
| 11    | `MsgHeightAck`                                          |
| 12/13 | reserved for cPoC `MsgSkipProbe` / `CarrySkip`          |


Inference stamps are additive proto3 fields on existing txs (zero / empty = unstamped). `InferenceRecordProto` fields 18/19 (`started_at_height` / `confirmed_at_height`) are omitted when zero, so unstamped `post_state_root` bytes do not change.

Nothing on the scheduling side — Interval, TurnTimeout, IdleTimeout, last turnover time — is folded into `Diff` or a `SyncTurnRecord`. Turn state remains a pure function of the log.

---

## Log plane vs edge


| Plane             | Height means                    | Rule                                                                                          | Replay                                              |
| ----------------- | ------------------------------- | --------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| Log (`Diff`)      | reference height / logical time | L0 against `F(m)`; L0b same-executor confirm ≤ finish; L1–L3 framing/sig/causality; L7 vector | yes — same INVALID                                  |
| Edge (envelope)   | first-party follower tip        | L4 binding (producer rule); L5a D band; (C-quorum)                                            | **no** — skipped on catch-up / gossip (`sec = nil`) |
| Oracle (deferred) | pair vs chain                   | L6 hash-at-H                                                                                  | mark only; not a diff verdict                       |


L5b (in-log `D` band) is **withdrawn**. Divergence between followers is monitoring. L5a may refuse an exchange and never invalidates a Diff.

---

## Behaviour that must not regress

- **Opt-in (hosts).** Unset `DEVSHARD_CHAINORACLE_URL` / `DEVSHARD_HEIGHTSYNC` ⇒ no envelopes, no heartbeats, existing inference path unchanged. Gateway stamps stay courier-only; `DEVSHARD_GATEWAY_CHAIN_ORACLE` defaults **off** and must not skip host seed.
- **Busy escrow pays nothing.** Host-stamped Start/Confirm/Finish discharges the cadence (H2, H33). The user's own stamp does not (H2 companion).
- **Quiet escrow still syncs.** A heartbeat turn opens within `Interval` of the last turnover (H1), **after** host-signed `F` exists. The gateway loop calls `MaybeHeartbeat`; span dispatch is concurrent and continues on one slot's error.
- **Repair never blames.** Missing ack ⇒ `HEIGHT` or `UNREACHABLE`; the turn stays `degraded` (H17–H20).
- **Arming emits nothing.** Silence past `T_idle` sets a local flag only (H21–H23). A missing ack is never a reason to arm.
- **Replay is deterministic.** Two verifiers with the same `Diff` compute the same `SyncTurnRecord`, the same `F(m)`, and the same L0–L3 / L7 verdicts. L4 and L5a need the envelope and are skipped on catch-up / gossip ingest. Admission refusals do not feed the floor (attack 24e).
- **Floor is host-only.** Sequencer heartbeats never raise `F`. Honest compose omits a host raise that does not match this hop's envelope.
- **No Strong, no slash.** Marks are local. Nothing in this PR produces a `LightBlock`, an evidence packet, or a dispute tx.

---

## Out of scope / next releases


| Deferred                                                                                 | Why it is not this PR                                                                                                                                                                          |
| ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Strong / `LightBlock` / `                                                                | Δ                                                                                                                                                                                              |
| Evidence packets, on-chain `MsgHeightSyncEvidence`, slashing, cross-session equivocation | Canonical half of a packet is a LightBlock. Marking lands here; adjudication does not.                                                                                                         |
| L6 as a verdict or a dispute                                                             | The pair is already in Diff when L6 can see it. INVALID would split verifiers whose oracles lag. Marks exist; a persistent recheck queue and dispute promotion do not. `F` is not rolled back. |
| cPoC skip-probe carriers (oneof 12/13 reserved only)                                     | Separate protocol.                                                                                                                                                                             |
| Switching `timeout.go` / the seal clock onto stamped heights                             | State-root change; needs a version gate after the stamps have been carrying.                                                                                                                   |
| Enabling height-sync on default compose                                                  | Opt-in until the sibling dapi mount and ops surface exist.                                                                                                                                     |
| Production dapi `/block/*`                                                               | Sibling `[ak/height-sync-protocol-dapi](https://github.com/gonka-ai/gonka/tree/ak/height-sync-protocol-dapi)`.                                                                                 |


Gateway `/metrics` and `GET /v1/debug/heightsync` (plan §8.12, E8) **did** land later on this branch: [#1621](https://github.com/gonka-ai/gonka/pull/1621). H26/H27 container + H39–H49.

---

## Config


| Env                                  | Default     | Effect                                                                |
| ------------------------------------ | ----------- | --------------------------------------------------------------------- |
| `DEVSHARD_CHAINORACLE_URL`           | unset       | HTTP oracle; empty ⇒ no height-sync on the host                       |
| `DEVSHARD_HEIGHTSYNC`                | unset       | 1/true enables chain-only height-sync without a dapi URL              |
| `DEVSHARD_HEIGHTSYNC_K`              | 10          | nonce cadence of Anchors (transport `K`, not the heartbeat interval)  |
| `DEVSHARD_HEIGHTSYNC_SLOTS`          | 1           | sync-turn width                                                       |
| `DEVSHARD_HEIGHTSYNC_PROBE_INTERVAL` | 30m         | re-probe dapi after a transport miss                                  |
| `DEVSHARD_GATEWAY_CHAIN_ORACLE`      | unset / off | Gateway local follower for trust labels only; must not skip host seed |


Runtime-config overlay (`Snapshot.HeightSync`, proto 13–18): zero means keep the compiled default. `Validate` requires `D_ack · BlockTime ≥ Interval + TurnTimeout`, `T_idle > Interval + TurnTimeout`, and `2 · Interval ≤ F` (originator freshness). An overlay that would fail is clamped to compiled defaults.

---

## Test plan

```bash
cd devshard
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test ./heightsync/... ./transport/... ./user/... ./host/... ./state/... ./chainoracle/...

# in-process e2e (held-response cases need -tags=dev)
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test ./testenv/scenarios/ -run HeightSync -count=1
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test -tags=dev ./testenv/scenarios/ -run HeightSync -count=1

# container (opt-in compose patch); two chats seed F before quiet Interval
make -C testenv citest-height-sync
```

Also `cd common && go test ./runtimeconfig/... ./nodemanager/...` for the ms overlay mapping.

Catalog: `[height-sync-tests.md](devshard/docs/height-sync-tests.md)`. Phases A–D are §2–§6. Log plane is §7. Floor / stamp: H2, H28–H31, H33, H54–H58, H89–H91. L6 marks only: H13e, H14, H57. Host-claim overlays A/B/C: `citest-height-sync` + `[heightsync_host_claims.feature](devshard/testenv/scenarios/heightsync_host_claims.feature)`.

Operator Gherkin: `[v5-manual-height-sync.md](devshard/docs/v5-manual-height-sync.md)`. Residual (warm cutover, host ping, proxy stamp): `[v5-manual-residual.md](devshard/docs/v5-manual-residual.md)`.