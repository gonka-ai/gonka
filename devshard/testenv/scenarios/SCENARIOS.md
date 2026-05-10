# Testenv scenarios (`devshard/testenv/scenarios`)

Go tests in this package exercise multi-component behavior. Run commands from
the `devshard` module root:

```bash
cd devshard
go test -count=1 ./testenv/scenarios/...
```

> **Container-level E2E plan.** The scenarios below run **in-process**
> (four `httptest` hosts, static `BlockOracle`). The plan to re-implement
> every scenario against the real `docker compose` stack (real
> `heightsyncd`, `mockdapi` SSE client, Loki / VM assertions) lives in
> [`CONTAINER_E2E_PLAN.md`](CONTAINER_E2E_PLAN.md). Both suites are
> intended to coexist: Go-only is the developer inner loop, the
> container suite is the CI gate.

## Height-sync anchor E2E (in-process HTTP)

**File:** `heightsync_anchor_e2e_test.go`  
**Tests:** `TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail`, `TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts`, `TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing`, `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`, `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`, `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors`, `TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns`

Detailed scenarios for **untrusted** (ahead-of-oracle) tips, audit **`Trust`** labels, and oracle **reconciliation** live in the section [Untrusted tip classification and oracle reconciliation](#untrusted-tip-classification-and-oracle-reconciliation) below (tests under `devshard/transport` and `devshard/heightsync`, not in this package).

### Flow covered in this single test

1. Build a four-host `httptest` stack with one static block oracle and
   height-sync schedulers configured as `K=8`, `slots=4`.
2. Verify wiring before traffic:
   - 4 servers and 4 host addresses exist,
   - oracle header is available,
   - height-sync audit rings are attached on all servers and user HTTP clients.
3. Send nonces `1..4` and assert initial sync turn behavior:
   - user outbound request anchors count is 4,
   - hosts emit 4 anchored first responses,
   - each host records inbound user anchor(s).
4. Continue with nonces `5..16` and assert cadence behavior:
   - strict per-nonce Anchor/Omit checks on user request payloads
     (`5..7` and `12..15` must omit; `8..11` and `16` must anchor),
   - scrape captured user/host height-sync logs:
     - user `mode` sequence matches nonce pattern,
     - each host's `mode` sequence matches only the nonces that host serves via round-robin,
     - user `block_hash_prefix` equals host `peer_block_hash_prefix` on anchored nonces,
   - total outbound request anchors are 9 (`1..4`, `8..11`, `16`),
   - hosts accumulate inbound user anchors across the full run.
5. Lost-first-response self-healing:
   - kill the host selected for nonce 1 after send starts and before reply is delivered,
   - send nonce 2 without restarting that host; nonce 2 still emits Anchor and receiving host emits Anchor on its first response,
   - by nonce 4, user audit ring contains at least one host attestation (no explicit height-sync RPC).
6. Mixed host-height carry-forward:
   - configure one host at `X+1` while other hosts and client start at `X`,
   - after client receives host attestation at `X+1`, subsequent in-turn user Anchors carry `X+1`,
   - receiving hosts store `X+1` in inbound audit-ring records even though their local oracle is still at `X` (those inbound Anchors are **ahead of** those hosts’ oracle tip—the same “untrusted height” situation the host labels **`untrusted_peer`** in audit and logs; see the section below for dedicated reconciliation tests).

### Cheating-trail — bogus user block hash at honest height (plan §9.3 item 7)

**Test:** `TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash`

The proof of concept does **not** verify Anchors against canonical mainnet; it only preserves what each peer claimed. This scenario proves that property end-to-end: a dishonest user can attach an Anchor whose **`mainnet_height`** matches what honest oracles use, but whose **`mainnet_block_hash_hex`** does **not** match the canonical `BlockID.Hash` at that height.

**Real situation:** A user wants to hide that they are lying about the chain tip while still participating in height-sync logging—e.g. to pollute the audit trail or frame another party later. They sign a normal inference envelope (still bound by existing transport auth) but substitute a fabricated hash for the real one at height `H`.

**Mechanism in the test:** The reference stack runs with four hosts and a static oracle at `(H, hash_canonical)`. `transport.ClientConfig.HeightSyncRequestMutateHook` runs **after** `AnchorScheduler.Decide` and peer-tip carry-forward and **only for nonce `1`** (inside the initial sync turn so an Anchor is emitted). The hook replaces `mainnet_block_hash_hex` with `hash_bogus ≠ hash_canonical` while leaving **`mainnet_height` unchanged**.

**Assertions:**

1. `SendInference` succeeds—the host must not reject the request (PoC accepts all Anchors).
2. Exactly **one** host audit ring (the round-robin target for nonce `1`) contains an inbound **`direction=request`** row for the user with `MainnetHeight == H` and **`MainnetBlockHash` equal to the bogus bytes** decoded from the wire—stored **verbatim**, not rewritten to the host oracle hash.
3. Trust classification remains **`peer_aligned`** because the claimed height is not strictly above the host oracle tip (same-height hash disagreement is **not** live-verified in the PoC; an offline verifier compares the ring row to canonical `(H, hash_canonical)` to detect cheating).

**Production:** leave `HeightSyncRequestMutateHook` nil; it exists only so tests can simulate a malicious client without reimplementing protobuf signing.

### Height-sync feed stopped — Omit during sync turn, no errors (plan §9.3 item 8)

In Docker testenv, **`heightsyncd`** is the HTTP publisher every `devshardd-testenv` **mockdapi** oracle subscribes to; killing that process makes **`Latest()`** fail for consumers (until reconnect/cache behavior kicks in). This package cannot start compose from a normal `go test`, so we model **“feed stopped for everyone”** with a single shared `blockoracle.BlockOracle` (`sharedStoppingOracle`) wired into **all four host schedulers + log oracles and the user client**. `SetStopped(true)` makes `Latest`/`At` return errors, matching “height-sync daemon gone.”

#### Implemented tests (`heightsync_anchor_e2e_test.go`)

| Test | What it proves |
|------|----------------|
| **`TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors`** | After nonce `1` (Anchor while feed is up), stop the feed; nonce `2` is still inside the **initial sync turn** (`1..slots_num` with `slots=4`) but **`AnchorScheduler.Decide`** degrades to Omit on oracle error (`devshard/heightsync/anchor.go`). Capture logger shows user `heightsync: emit` **`mode=omit`** for the second request and host **`peer attestation received`** **`mode=omit`** on the inbound side. `SendInference` returns **no error**; outbound request-anchor count does not increase after nonce `2`. |
| **`TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns`** | With feed stopped, nonces `2..7` all Omit (covers tail of initial window + Omit cadence before next periodic window). `SetStopped(false)`; nonce **`8`** begins periodic sync turn `{8..11}` → user emit **`mode=anchor`** again and request-anchor count reaches **2**. |

Helper: **`setupFourHostHTTPHeightSyncStoppingOracle`** — one `newSharedStoppingOracle` pointer shared across hosts and user.

#### Proposed follow-ups (not implemented here)

1. **Compose / integration:** `docker compose up`, drive `devshardctl` or HTTP client against real `devshardd-testenv`, **`docker stop height-sync`**, assert the same log markers from container logs (or a metrics sidecar). Highest fidelity for ops.
2. **Partial outage:** only **some** hosts lose the feed (per-host oracle wrapper) while others keep it — document expected omit vs anchor split (product decision).
3. **Stale cached header:** mockdapi client may retain a **cached** header after SSE drops — extend tests if `StaleAfter` / reconnect semantics should emit Anchor once from cache then Omit (align with `devshard/testenv/mockdapi` behavior).

### Manual-force Anchor — single-message trigger (plan §9.3 item 6, current)

> **Status:** implemented but only covers the **trigger** plumbing.
> The full cPoC manual-force semantics — *force a complete sync turn,
> not a single envelope* — are tracked in the next section
> ("Manual-force forced sync turn") and depend on `MsgForceHeightSyncTurn`
> (plan §5.5).

**Test:** `TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn`

**Wire:** `user.InferenceParams.ForceHeightSyncAnchor` → `host.HostRequest` → `transport.HTTPClient.Send` passes `heightsync.DecideHints{ForceAnchor: true}` so an Anchor is emitted even when `AnchorScheduler.Decide` would normally return Omit for that nonce.

**Scenario:**

1. Same four-host stack as the cadence test (`K=8`, `slots=4`).
2. Send nonces `1..6` with normal `SendInference` (no force). Nonces `5` and `6` are Omit on the **user request** path.
3. Nonce `7` would also Omit under cadence (`7` is not in `{1..4} ∪ {8..11} ∪ …`).
4. Send nonce `7` with `ForceHeightSyncAnchor: true` on `InferenceParams`.
5. Assert:
   - user outbound request-anchor count increases by exactly **one** vs step 2 (forced protobuf Anchor on nonce 7);
   - the host that serves nonce `7` (`hostIdx = 7 % 4`) records **one** additional inbound user Anchor in its height-sync audit ring.

**Transport-only coverage** (single-host `httptest`, no session): `TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest` asserts `InferenceRequest.force_height_sync_anchor` forces an Anchor on the **SSE receipt** when `responseNonce` would otherwise Omit (`K=10`, `slots=1`). JSON field round-trip: `TestHostRequest_ForceHeightSyncAnchor_TransportJSONRoundTrip`.

> **Limitation observed during review:** the forced Anchor only
> applies to **one envelope (one slot)**. Other hosts in the
> round-robin still see Omit on adjacent nonces, so the group does
> **not** converge on the same `(height, hash)` the way a regular
> cadence sync turn would. This is the gap the next section closes.

### Manual-force forced sync turn (plan §5.5, PROPOSED)

> **Status:** **planned** — depends on `MsgForceHeightSyncTurn` and
> escrow-state field `ForcedHeightSyncTurn{Active, StartNonce,
> EndNonce, Reason}`. Not yet implemented; this section is the
> contract the implementation has to satisfy. See
> `devshard/plans/height-sync-anchor-poc.md` §5.5 and
> `height-sync-anchor-poc-implementation-status.md` Step 9.

**Wire summary:**

- The trigger is **a single diff tx** (`MsgForceHeightSyncTurn`),
  carried inside `DevshardTx`. **No per-host HTTP signal is part of
  the protocol contract.** Hosts learn the forced window by
  applying the diff just like every other state-machine fact;
  divergent users cannot opt some hosts in and others out.
- `InferenceParams.ForceHeightSyncAnchor=true` (and the matching
  host-side `InferenceRequest.force_height_sync_anchor`) is a
  **convenience for the honest reference client** that asks
  `composeDiffLocked` to insert that diff tx. It is not an
  authoritative wire signal: a malicious user can omit it and the
  network is unaffected because either (a) no
  `MsgForceHeightSyncTurn` ever lands and there is no forced turn,
  or (b) one diff lands and every host applies the same window.
- The state machine sets/clears `ForcedHeightSyncTurn` from
  `applyTx(MsgForceHeightSyncTurn)` and `applyCore`.
- Both `transport.HTTPClient.Send` (user) and
  `transport.Server.HandleInference` (host) read the active
  `ForcedHeightSyncTurn` snapshot from their **own** state machine
  and pass it via `heightsync.DecideHints.ForcedTurn`. Crucially
  the **host** reads its own state, so its response Anchors are
  driven by the diff, not by the inbound HTTP envelope.
- `AnchorScheduler.shouldEmit` returns Anchor **for every nonce in
  `[StartNonce, EndNonce]`**, swallowing any cadence window that
  touches/overlaps the forced one (no double-Anchor at the boundary).

**Trust model for forced-turn Anchors:**

- **Host responses inside `[StartNonce, EndNonce]`** are normatively
  bound — the receipt MUST carry Anchor. Any host that sends an
  Omit receipt inside the window has provably misbehaved (the
  user's audit ring records the omission against the host's
  signed receipt).
- **User requests inside `[StartNonce, EndNonce]`** are
  **best-effort**. The honest client emits Anchor; a malicious
  user that omits Anchor on its in-window requests is **not**
  rejected at the receiving host. Hosts SHOULD log
  `height_sync_force_request_anchor_missing` warn entries (with
  `nonce`, `peer_id`, `forced_start`, `forced_end`) to the audit
  ring as dispute evidence, but processing continues. This
  mirrors the diff being the single source of truth: the user
  signed the diff that opened the turn, so failing to Anchor on
  the wire is *self-inflicted*, not an attack on the network.

#### Scenario A — `TestHeightSyncAnchor_E2E_ForcedSyncTurn_AnchorsEntireSlotWindow`

Locks the core "all slots get Anchor (host-response side, normative)
property under the **honest-user** assumption". Combined with
Scenario E (below) it covers the trust model end-to-end.

1. Stack: four hosts, `K=8`, `slots=4`. Static oracle pinned to a
   single `(H, hashH)`.
2. Warm-up: run nonces `1..6` with no force. Cadence Anchor window
   is `{1..4}` (initial sync turn). Nonces `5,6` are Omit on user
   request. Snapshot baseline counts:
   - per-host inbound user Anchors,
   - per-host outbound response Anchors,
   - user-side inbound host Anchors.
3. On nonce `7`, send with `InferenceParams.ForceHeightSyncAnchor =
   true`. Expectation: the diff composer emits
   `MsgForceHeightSyncTurn{trigger_nonce: 7, end_nonce: 10,
   reason: "manual"}` exactly **once**, in the diff at nonce 7
   only. Subsequent diffs (8..10) carry no force tx. Every host
   that applies the trigger diff updates its escrow state to
   `ForcedHeightSyncTurn{Active: true, Start: 7, End: 10}`.
4. Continue the round-robin for nonces `7,8,9,10` with **plain**
   `SendInference` (no further force). Each request travels to
   `hostIdx = nonce % 4 = {3, 0, 1, 2}` — i.e. exactly one full
   sweep over all four hosts.
5. Assertions, all measured deltas vs the baseline at end of step 2:
   - **Diff-level**: only diff 7 contains a `MsgForceHeightSyncTurn`;
     diffs 8..10 do not. (Locks the "single message in the chain"
     invariant.)
   - **Host→user response Anchors (normative)**: each host's
     outbound response-Anchor counter increases by exactly **one**;
     user-side inbound host-Anchor counter increases by `+4` over
     the four servers. This is the property the network actually
     enforces — driven by each host reading its **own** escrow
     state, not by anything the user puts on the wire.
   - **User outbound request Anchors (best-effort, honest user)**:
     increases by `+4` because the reference client honors the
     forced window via `EscrowHeightSyncHints`.
   - **Each host's inbound user-Anchor counter (best-effort,
     honest user)**: `+1` per host. This assertion is **conditional
     on the honest reference client**; a malicious-user variant
     where these go to `0` is covered by Scenario E.
   - On nonce `11`, the request is Omit (forced turn ended at `10`,
     and `11` is not yet inside the cadence window `{8..11}` either
     — see Scenario C for the coalescing case).

#### Scenario E — `TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits`

Locks the trust model: **the diff alone forces all host responses
to Anchor; a malicious user can drop request Anchors but cannot
prevent host alignment.**

1. Same stack as Scenario A. Warm-up `1..6` as before.
2. Trigger the forced turn at nonce `7` with
   `ForceHeightSyncAnchor = true` so `MsgForceHeightSyncTurn`
   lands in diff 7. **Then disable** the client's
   `EscrowHeightSyncHints` plumbing for nonces `7..10` (test
   helper that nils out `HostRequest.HeightSyncEscrow` on Send,
   simulating a malicious user that strips `height_sync` from its
   in-window requests).
3. Continue nonces `7..10` and let nonce `11` close out.
4. Assertions:
   - **Diff invariant unchanged**: only diff 7 carries
     `MsgForceHeightSyncTurn`.
   - **Host→user response Anchors**: each host still emits Anchor
     on its receipt within `[7, 10]`; user-side audit ring still
     gains `+4` host attestations. (Forced-turn enforcement is
     server-side, driven by the host's own state.)
   - **User outbound request Anchors**: `0` over `7..10`
     (malicious user, by construction).
   - **Each host's inbound user-Anchor counter**: unchanged from
     baseline (`0` deltas) — receiving hosts saw plain JSON, no
     `height_sync` section, classified as Omit on inbound, **and
     served the request normally** (no rejection).
   - **Per-host audit warn entries**: each host that served an
     in-window request records a structured
     `height_sync_force_request_anchor_missing` warn line keyed by
     `(escrow_id, nonce)` with the inbound peer id and the active
     `[StartNonce, EndNonce]`. Asserting at least one such entry
     per served host locks the dispute-evidence path.
   - Cadence resumes from `i*K` after the turn closes
     (interaction with Scenario C is orthogonal).

#### Scenario B — `TestHeightSyncAnchor_E2E_ForcedSyncTurn_IgnoresReentryWhileActive`

Locks the *"any `ForceHeightSyncAnchor` inside a non-finished sync
turn is ignored"* rule.

1. Same stack and warm-up as Scenario A.
2. Open a forced turn at nonce `7`
   (`InferenceParams.ForceHeightSyncAnchor = true`).
3. On nonce `8` (still inside `[7, 10]`), send another request with
   `ForceHeightSyncAnchor = true`. Expectation:
   - the diff composer **does not** add a second
     `MsgForceHeightSyncTurn` to the diff; if a buggy implementation
     did, the state machine drops it on `applyTx` (no-op) and
     `ForcedHeightSyncTurn.{StartNonce, EndNonce, Reason}` are
     unchanged.
   - The forced window still ends at `EndNonce = 10`, **not** at
     `11` (no extension).
4. Assertions:
   - Anchor counts deltas across nonces `7..10` are identical to
     Scenario A (`+4` request, `+4` response, one per host).
   - Nonce `11` is Omit (turn closed at `10`).
   - Audit-ring `Reason` for the forced-turn entries equals the
     reason from the **first** trigger, not the second.

#### Scenario C — `TestHeightSyncAnchor_E2E_ForcedSyncTurn_CoalescesWithPlannedCadence`

Locks coalescing with a planned cadence window — *"if a manually
forced sync turn intersects with a planned cadence Anchor, the
planned one is skipped in favor of the manual sync"*.

1. Stack: four hosts, `K=8`, `slots=4`. Cadence Anchor windows are
   `{1..4}` (initial) then `{8..11}` (first periodic).
2. Warm-up nonces `1..4` (initial cadence Anchor; baseline Anchor
   counts captured at end of nonce 4).
3. Run nonces `5,6` with no force (Omit). Nonce `7` triggers the
   forced turn → `ForcedHeightSyncTurn = {Start: 7, End: 10}`. Note
   that `End=10` lies **inside** the upcoming cadence window
   `{8..11}` — they overlap on nonces `8,9,10`.
4. Continue nonces `7..11` with plain `SendInference`.
5. Assertions:
   - Anchor counts on nonces `7..10` match Scenario A (forced turn
     active, every host gets one).
   - **Nonce `11`** would normally be the last nonce of the cadence
     window `{8..11}`. Per the coalescing rule, the cadence window
     for this period is **swallowed** by the forced turn — so the
     scheduler emits **Omit** on nonce `11`. Concretely: total
     Anchor count over `7..11` is **4**, not 5.
   - The next cadence window opens at `2*K = 16` and runs
     `{16..19}` unaffected.

#### Scenario D — `TestHeightSyncAnchor_E2E_ForcedSyncTurn_RestartCadenceAfterClose`

Sanity case: forced turn lying strictly between two cadence windows
must not affect them.

1. Same stack as Scenario A. Cadence windows `{1..4}` and `{8..11}`.
2. Trigger the forced turn at nonce `5` → window `[5, 8]`. Nonces
   `5..8` Anchor under the forced turn.
3. Nonce `9..11` are inside the cadence window `{8..11}` — but
   nonce `8` was **already** Anchored by the forced turn
   (coalescing applies between forced-end and cadence-start when
   they touch); nonces `9, 10, 11` Anchor under cadence as normal.
4. Nonces `12..15` Omit. Cadence resumes at `{16..19}`.
5. Assertions: total Anchor count over `5..15` equals `8`
   (`{5..11}` continuous Anchors + nothing on `12..15`); per-host
   inbound user-Anchor delta over `5..11` is `≥ 1` for all four
   hosts (each host hit at least once).

#### Implementation hooks the e2e exercises

- `MsgForceHeightSyncTurn` round-trips through
  `proto/devshard/v1/diff.proto` and `devshard/types/diff.pb.go`.
- Escrow / session state exposes
  `ForcedHeightSyncTurn{Active, StartNonce, EndNonce, Reason}` to
  `transport` so `DecideHints.ForcedTurn` is populated on every
  send/receive.
- `AnchorScheduler.shouldEmit` honors `ForcedTurn` (unit-tested
  separately in `heightsync/anchor_test.go` —
  `TestAnchorScheduler_ForcedTurn_*`).
- Diff composer in `devshard/user` is the only place that
  **opens** a forced turn (when the trigger flag is set and no
  forced turn is active). All other call sites that look at the
  trigger flag must treat it as a no-op while a turn is active.

#### Run these scenarios only

```bash
cd devshard
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_ForcedSyncTurn_'
go test -count=1 -v ./heightsync          -run '^TestAnchorScheduler_ForcedTurn_'
go test -count=1 -v ./transport           -run '_HeightSync_ForcedTurn_'
```

### Run this scenario only

```bash
cd devshard
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail$'
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts$'
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing$'
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash$'
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_'
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn$'
# Forced sync turn (planned, §5.5):
go test -count=1 -v ./testenv/scenarios -run '^TestHeightSyncAnchor_E2E_ForcedSyncTurn_'
```

## Untrusted tip classification and oracle reconciliation

These scenarios are implemented as **unit / integration tests** in **`devshard/heightsync`** and **`devshard/transport`** (run from `cd devshard`). They are not part of the `testenv/scenarios` package, but they define the expected behavior when a host must distinguish **oracle-backed** vs **ahead-of-oracle peer** `(height, hash)` and when the oracle later catches up to the same height.

Anchor-related debug lines include **`trust_level`**: `trusted_oracle`, `untrusted_peer`, `peer_aligned`. Full semantics: `devshard/plans/height-sync-anchor-poc.md` §6.2.

### Concepts

- **`trusted_oracle`:** The host’s **outbound** Anchor on SSE is filled from the local block oracle (`Latest()`). Audit rows for those emissions use this label.
- **`untrusted_peer`:** An **inbound** user Anchor whose **`mainnet_height` is strictly greater** than the host’s oracle height **at receipt time**. The host accepted a newer tip from the user/peers before its own oracle reached that height.
- **`peer_aligned`:** An **inbound** Anchor at or below the local oracle height (peer not strictly ahead of oracle).

### Reconciliation at the same height: hash **matches** vs **mismatches**

After an **`untrusted_peer`** Anchor at height **H** is stored, the host keeps **pending** `(H, block_hash_from_peer)`. When **`Latest()`** later returns **the same H**, the host compares bytes:

| Case | What the test sets up | How you know it worked |
|------|------------------------|-------------------------|
| **Match** | Oracle reaches **H** and its **`BlockHash`** **equals** the hash from the earlier user Anchor. | Second inference: **no** `Warn` about disagreeing tips. Test: **`TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn`**. |
| **Mismatch** | Oracle reaches **H** but **`BlockHash` ≠** the pending peer hash at that height. | Second inference: **one** `Warn` with message containing `untrusted peer tip disagrees with oracle at reconciled height` (and structured fields `oracle_block_hash_prefix` / `untrusted_block_hash_prefix`). Test: **`TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns`**. |

Both are **transport** tests: they drive two `POST …/chat/completions` on the **same** session, mutating a shared test oracle between requests. They are the supported way to assert match vs mismatch behavior (the E2E carry-forward test does not flip oracle hashes to force these cases).

### Scenario: inbound trust mapping (`TestInboundTrust`, package `heightsync`)

Asserts that **`heightsync.InboundTrust`** returns **`untrusted_peer`** when the Anchor height is above the oracle header height, and **`peer_aligned`** when it is at or below—no HTTP stack required.

### Scenario: oracle catches up with a different hash → Warn (`TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns`, package `transport`)

1. Point the host’s oracle at height **H₀** (e.g. 10). Send an inference whose wrapped envelope includes an Anchor at height **H₁ > H₀** (e.g. 11) and a chosen **`mainnet_block_hash_hex`**.
2. The host classifies that inbound Anchor as **`untrusted_peer`**, records it in the audit ring, and stores a **pending** `(H₁, hash)` for that escrow session.
3. Advance the **same** oracle used by the host so it now reports height **H₁** with a **different** **`BlockHash`** than the pending peer hash.
4. Send a **second** inference on the same session. Before handling the new body, the host compares oracle vs pending; it must emit **exactly one** **`Warn`** (`heightsync: untrusted peer tip disagrees with oracle at reconciled height`) and then clear pending.

### Scenario: oracle catches up with the same hash → no Warn (`TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn`, package `transport`)

Same as the mismatch scenario through step 2, but when the oracle reaches **H₁**, its **`BlockHash`** **matches** the hash from the user’s earlier Anchor. The follow-up inference must complete **without** that Warn.

### Scenario: outbound Anchor stays oracle-trusted (`TestServer_Inference_HeightSync_OutboundAnchor`, package `transport`)

Height-sync enabled on the server; first receipt SSE carries **`height_sync`** from the scheduler/oracle; the host audit ring’s **`response`** row has **`Trust=trusted_oracle`**.

### Relation to E2E `TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts`

That E2E scenario exercises **carry-forward**: after one host attests **`X+1`**, the user sends **`X+1`** to other hosts whose oracle is still **`X`**—so those hosts observe **ahead-of-oracle** inbound Anchors end-to-end. It does **not** mutate a single host’s oracle through conflicting hashes to assert **`Warn`**; use **`UntrustedReconcileMismatchWarns`** / **`MatchNoWarn`** for reconciliation behavior.

### Run untrusted / reconciliation tests only

```bash
cd devshard
go test -count=1 -v ./heightsync -run '^TestInboundTrust$'
go test -count=1 -v ./transport -run 'TestServer_Inference_HeightSync_(OutboundAnchor|UntrustedReconcile)'
```

**Match vs mismatch only:**

```bash
cd devshard
go test -count=1 -v ./transport -run '^TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn$'
go test -count=1 -v ./transport -run '^TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns$'
```

## Client-side malware-host detection and Δ semi-trust (proposed)

> Status: **proposed, not yet implemented.** These scenarios extend the host-side
> behavior already covered above to the user (`devshard/transport/HTTPClient`,
> `devshard/user/httpsession.go`) and cover the **trust model** from
> [`devshard/docs/proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md`](../../docs/proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md):
> Anchor is **semi-trusted only inside `|Δ| ≤ D`**; out-of-band claims require **Strong** (`LightBlock` + `VerifyCommit`).
> Tracked under § 12 Follow-ups in `devshard/plans/height-sync-anchor-poc.md`.

### Why this is needed

Today the user keeps a single shared **highest-tip** in `transport.HeightSyncPeerTips` and **carries it forward** into the next outbound Anchor (so a tip learned from host A is gossiped to hosts B/C/D inside the same sync turn). That fixes the round-robin alignment problem but creates a new gap:

- A **malware host A** can attest `(H, hash_BAD)`. The user carries that into requests to B/C/D and stores `(baseURL=A, H, hash_BAD)` in its own audit ring.
- When the user’s **own** local oracle later confirms `(H, hash_GOOD)`, nothing today identifies A as the source. Hosts B/C/D only know "the user told me `H, hash_BAD`" — they cannot blame A either.
- The current carry-forward is also **unbounded**: any peer height (`H_peer >> H_local`) is propagated. The proposal §188 explicitly requires **Anchor INVALID when `|H_peer − H_local_aligned| > D`** (default `D = 2`); above the bound, the host **MUST** switch to Strong (`LightBlock`).

### Concepts to add

| Name | Meaning |
|------|---------|
| **`D` (delta threshold)** | Max allowed `|H_peer − H_local_aligned|` for an Anchor to be accepted as semi-trusted. Default `D = 2` (proposal §16, §188). Configurable per deployment under `height_sync.strong_threshold_d`. |
| **`Provenance`** on a carried tip | The original `peer_id` (host `baseURL` / signer address) that first attested `(H, hash)`. Stored alongside the tip in `HeightSyncPeerTips` and replicated into the user's outbound audit row when carry-forward happens. |
| **`mode=full` / Strong fetch** | A new host RPC (§5.3 of the plan, currently deferred) returning a `LightBlock` + signed `(H, hash)` so the user can **verify** Strong out-of-band when an Anchor falls outside `D`. |
| **`DEFERRED_FAIL` evidence** | When the user’s oracle reaches `H` and any audit row with that `H` has a hash that disagrees with canonical `BlockID.Hash`, emit a structured warning naming the **originating** `peer_id`. |

### Proposed scenarios (test names suggested)

#### 1. `TestHTTPClient_DeferredVerify_IdentifiesMalwareHost`

Goal: prove the user can identify the malware host once its own oracle catches up.

1. Set up 4 in-process hosts and one user. The user’s **own** oracle is at `H₀`.
2. Host A (malware) responds with Anchor `(H₀+1, hash_BAD)`. Hosts B/C/D respond with `(H₀, hash_GOOD)` or stay omit.
3. The user’s `HeightSyncPeerTips` records the highest tip with **provenance = host A**. Later requests in the same sync turn carry `(H₀+1, hash_BAD)` to B/C/D.
4. Advance the user’s oracle to `H₀+1` with **canonical** `hash_GOOD`.
5. The user runs **deferred verification** over its audit ring (and the in-flight tip): for every `(peer_id, H, hash)` where `H ≤ oracle.height` and `hash ≠ canonical_hash`, emit `Warn` `heightsync: deferred verification mismatch (malware host suspected)` with structured fields `peer_id=<host A>`, `height=H₀+1`, `expected_hash_prefix`, `received_hash_prefix`.
6. Assert: exactly **one** Warn, naming **host A**, regardless of how many hosts the user later forwarded the bad tip to.

#### 2. `TestHTTPClient_CarryForward_ProvenanceRecorded`

1. Host A attests `(H, hash_X)`; user’s peer-tips updates with `Provenance=A`.
2. User sends next request to host B; carry-forward injects `(H, hash_X)` and the **outbound audit row** records `OriginPeerID=A` (new field on `AnchorAttestation` or a sibling structure on the user side).
3. Assert: the user’s audit ring shows the outbound `request` row at `(H, hash_X)` with `OriginPeerID=A`, even though the request was sent to B.

#### 3. `TestHTTPClient_DeltaBound_RejectsCarryForward`

1. User oracle at `H_local`. Host A attests `(H_local + D + 1, hash_X)` (i.e. `|Δ| > D`).
2. Assert: the user **does not** carry that tip forward (`HeightSyncPeerTips` does not update beyond `D`); user emits `Warn` `heightsync: anchor outside delta bound, strong required` with `peer_id=A`, `delta=D+1`, `bound=D`.
3. Assert: the next outbound Anchor in the same sync turn falls back to the **user’s own oracle** value (`(H_local, hash_local)`), not the bogus `(H_local + D + 1, hash_X)`.

#### 4. `TestHTTPClient_DeltaBound_TriggersStrongFetch` (depends on Step 5 RPC)

1. Same setup as (3) but with the explicit height-sync `mode=full` RPC available.
2. On detecting `|Δ| > D`, the user calls `HostClient.FetchHeightSync(ctx, mode=full)` against host A.
3. If host A returns a valid `LightBlock` whose `BlockID.Hash == hash_X`, the user advances its **strong** anchor state and accepts `(H_local + D + 1, hash_X)`.
4. If host A returns no proof or a proof that fails `VerifyCommit`, the user records **dispute evidence** (`peer_id=A`, structured `(H, hash, reason)`), and the carry-forward path stays at `(H_local, hash_local)`.

#### 5. `TestHTTPClient_CrossHostDisagreementInSyncTurn`

1. During the **initial** sync turn nonces `1..slots_num`, four hosts respond. Three agree on `(H, hash_GOOD)`; one returns `(H, hash_BAD)`.
2. The user’s audit ring stores all four rows. Without waiting for the local oracle, a heuristic surfaces a `Warn` `heightsync: peer disagreement at same height` listing the **odd-host-out** peer id and its hash prefix.
3. When the oracle later confirms `H`, scenario (1) fires for the same host as definitive evidence.

#### 6. `TestHTTPClient_DeferredVerify_ClearsOnMatch`

Negative companion to (1): when the oracle catches up and the audit-ring hash **matches** canonical `BlockID.Hash`, the user emits **no** Warn and clears any pending row for that height.

#### 7. `TestUserHTTPSession_PropagatesProvenanceAcrossClients`

Session-level: a single `HTTPSession` instantiates one shared `HeightSyncPeerTips` across multiple `HTTPClient`s (current code already does this). Add a test that asserts **provenance** survives the round-robin: the tip carried into the request to host B still carries `OriginPeerID=A`.

### Same-height/different-hash carry-forward → dispute trigger

> Status: **proposed, not yet implemented.** This case is the immediate
> (oracle-not-required) version of scenarios (1) and (5). It is what the
> proposal calls `DEFERRED_FAIL` collapsed into the inference path when the
> receiving host **already has block H locally** (proposal §188 / §202).

#### Setup

1. On nonce **N**, the user sends a request to host A. A's response carries a **signed** Anchor `(H, hash_A)` (proposal §101–115 `sender_signature`).
2. The user's `HeightSyncPeerTips` stores `(H, hash_A, OriginPeerID=A, originAttestation=signed_blob_from_A)`.
3. On nonce **N+1** (still in the same sync turn), the user round-robins to host B and **carries forward** `(H, hash_A)`.
4. Host B's local oracle already knows height `H` with `BlockID.Hash = hash_B`, and `hash_B ≠ hash_A`.

#### Expected behavior — **dispute trigger**

- **Host B** classifies the inbound Anchor:
  - It first verifies the **`sender_signature`** field carried in the envelope.
  - If the signature is **present** and verifies against **host A's** registered key for chain_id, B logs `Warn` `heightsync: same-height hash disagreement, dispute opened (origin=host)` with `origin_peer_id=A`, `height=H`, `expected_hash_prefix=hash_B`, `received_hash_prefix=hash_A`. B records the carried row in its audit ring with `Trust=peer_aligned` (height matches local) **plus** a `DisputeOrigin=A` field. The signed `(H, hash_A)` blob is preserved verbatim so a finalizer can replay it as evidence against host A.
  - If the signature is **missing** or **fails verification** (forged / wrong key / wrong signing input), B treats the claim as **originating from the user** and logs `Warn` `heightsync: same-height hash disagreement, dispute opened (origin=user)` with `origin_peer_id=<user signer>`. The audit row records `DisputeOrigin=<user>`. Network policy: the **user is punished** because they could not produce signed evidence binding `(H, hash_A)` to host A.
- The dispute does **not** abort the inference: the application body still flows; only the height-sync state is treated as evidence material.
- The receiver's reconciliation rule still applies on later inferences: when the host's oracle advances and `BlockID.Hash` confirms `hash_B`, the audit row containing `hash_A` is upgraded to `DEFERRED_FAIL` evidence with `accountable_peer_id=A` (or `=user` when no valid signature was attached).

#### Why the user must store the signed attestation

The carry-forward path injects a peer's `(H, hash)` into a request the user is **signing as the user**. Without `sender_signature`, host B cannot tell whether `(H, hash_A)` came from a real attestation by host A or whether the user **fabricated** it (cheap to do — the user already signs the request envelope). The proposal already requires Anchor sections to carry the originator's signature; the carry-forward path **MUST** propagate that signature byte-for-byte (not re-sign with the user's key). This gives the network the cryptographic trail needed to assign blame:

| What the user can show during dispute | Network treats it as |
|----------------------------------------|----------------------|
| Original signed Anchor blob from host A for `(H, hash_A)` | Evidence against **host A** (A misrepresented `H` or hash). |
| No signed blob, or signature does not verify against A | Evidence against **the user** (user fabricated or replayed the height claim). |
| Signed blob whose payload (`session_id`, `nonce_num`, `direction`) does not match the carried-forward request context | Evidence against **the user** (replay across sessions / directions is treated as fabrication). |

#### Proposed test names

- `TestHTTPClient_CarryForward_PreservesOriginatorSignature` — assert the wrapped envelope sent to host B includes the **exact** signed `(H, hash_A)` blob received from host A in the prior response (no re-signing by user).
- `TestServer_Inference_HeightSync_DisputeOriginHost_WhenSignatureValid` — host B receives the wrapped envelope with `H_local == H` and `hash != hash_local`; signature verifies as host A → one Warn `(origin=host)` and an audit row with `DisputeOrigin=A`.
- `TestServer_Inference_HeightSync_DisputeOriginUser_WhenSignatureMissing` — same setup but the user did not propagate `sender_signature` → Warn `(origin=user)` and `DisputeOrigin=<user signer>`.
- `TestServer_Inference_HeightSync_DisputeOriginUser_WhenSignatureForged` — `sender_signature` is present but does not verify against host A's key → Warn `(origin=user)`.
- `TestServer_Inference_HeightSync_DisputeOriginUser_WhenReplayedAcrossSession` — signed blob is for a different `session_id` / `nonce_num` than the request that carries it → Warn `(origin=user)`.
- `TestUserHTTPSession_StoresSignedAttestationForDispute` — after each inbound host SSE Anchor, the user retains the raw signed blob (not just the parsed `(H, hash)`) until the corresponding height is finalized or evicted from the bounded ring.

### Suggested implementation hooks

- Extend `HeightSyncPeerTips` to store `(*HeightSyncSection, originPeerID string, originSignedBlob []byte, observedAt time.Time)` instead of just the section. `Update` records the originator and signed blob on first observation; `Carry` exposes both to the caller. The signed blob is the proposal's `HeightSyncSection` serialization that the originator (host) signed under §101 — verbatim, not re-signed by the user.
- When the user emits an outbound Anchor whose `(H, hash)` came from carry-forward (not from the user's own oracle), the wrapped envelope **MUST** include the originator's signed blob. The user's transport-level `Sender` signature is **separate** and covers the request envelope only — it does not authenticate `(H, hash)`.
- Add `OriginPeerID` and (server-side) `DisputeOrigin` to `heightsync.AnchorAttestation` (optional, only set when carry-forward happened or a dispute fired). Existing audit assertions stay valid because both fields are empty when the user’s **own** oracle filled the section.
- On the host (`transport.Server`), extend the inbound classifier:
  - When the inbound Anchor is at `H ≤ local_oracle.height` but `hash != local_oracle.BlockHash`, run signature verification on the carried originator blob. Decide `DisputeOrigin` (`<host A>` vs `<user>`) by signature outcome. Persist both the decision and the raw signed blob (or a content-addressed hash + archive) so a finalizer can replay it.
  - Same-height/same-hash case stays silent (no Warn).
- Add a client-side `RunDeferredVerification(ctx)` walker that iterates the audit ring against `Latest()` whenever the user’s oracle advances past the highest pending peer height; this is the user-side equivalent of the host’s `reconcilePendingUntrusted`. The walker emits `DEFERRED_FAIL` evidence keyed by `OriginPeerID` (or `<user>` when no valid attestation was preserved).
- Configurable knob `height_sync.strong_threshold_d` (default `2`) plumbed alongside `anchor_period_nonces` and `sync_turn_slots` (see `devshard/testenv/config/config.go`).
- Tracking only — Step 5 explicit RPC is already deferred; scenarios (4) and forensic snapshots become its first concrete users.

## Config presence (testenv layout)

**File:** `sanity_test.go`  
**Test:** `TestScenarios_ConfigPresent`

This test loads `devshard/testenv/config.yaml`, validates parsing and config
constraints, and checks `height_sync.validators` is non-empty.

```bash
cd devshard
go test -count=1 -v ./testenv/scenarios -run '^TestScenarios_ConfigPresent$'
```
