# Proposal: cap the heartbeat index behind L3

**Status:** Draft / proposal
**Related:** [HEIGHT_SYNC_PROTOCOL_PROPOSAL.md](HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) (log plane, L0–L7), [../diff-catchup-memory-plan.md](../diff-catchup-memory-plan.md) (snapshot restart replays the full journal).
**Scope:** `heightsync.TurnTracker` (`heartbeatAt`, `prune`, `Clone`), log-plane `checkL3` and `checkL7`, gateway compose in `state.localBestEffortLocked`, `heightsync.FleetCompat`.

This is a design note. It does not change code by itself.

---

## Problem

`TurnTracker.heartbeatAt` maps every heartbeat nonce to the start of its turn. `prune` keeps the newest `DefaultTurnRetain` (64) turn records and deliberately leaves `heartbeatAt` alone, so the map gains one entry per heartbeat for the life of the escrow.

The gateway opens a turn every `DefaultHeartbeatInterval` (24 s) whenever it has a chain height, including in a quiet session. A turn writes one heartbeat per slot (`Heartbeat.SpanTxs`).

| Session length | Group of 8 | Group of 32 |
| --- | --- | --- |
| 1 day | 28,800 entries | 115,200 entries |
| 30 days | 864,000 entries, about 17 MB | 3.5 million entries, about 70 MB |

The map is part of `mutableSnapshot`. `TurnTracker.Clone` copies all of it, and `ValidateDiff` and `PreviewLocalBestEffort` take two snapshots per diff. Each diff therefore allocates about twice the map and throws most of it away. Every host and the gateway keep their own copy.

The snapshot blob does not store the map. `foldHeightSync` rebuilds it on restart by replaying the journal from nonce 1, so a restart does not shrink it.

---

## How `heartbeatAt` is used

A heartbeat is a user-signed `MsgHeartbeat`. A turn is `slots_num` of them on consecutive nonces, and the turn is named by the nonce its first heartbeat lands at. A host answers the heartbeat addressed to its slot with a `MsgHeightAck` whose `ref_nonce` is that heartbeat's nonce. The ack carries no turn id.

`heartbeatAt` has three readers:

1. **L3** (`checkL3`, through `HeartbeatTurn`): asks only whether `ref_nonce` is a key. The stored turn start is not read.
2. **Folding acks into turns** (`observeAck`): looks up the turn start and adds the ack to that turn record. When the turn record has already been pruned, the ack is dropped.
3. **Attribution** (`turnIndex.forAck`): names the turn on L4, L5a, L6 and L7 marks, and groups acks by turn for the L7 vector check.

## What L3 checks

For every ack in a diff, `ref_nonce` must be a heartbeat nonce, either in the same diff or already applied. If it is not, the whole diff is rejected with `ErrAckCausality` and the nonce is not consumed.

L3 does not check that the ack is on time, that it came from the addressed slot, or that the turn record still exists.

L3 exists because `ref_nonce` is trusted downstream:

- L0 judges the ack's height against the floor as of `ref_nonce + 1`, and skips the comparison when that floor is unknown or zero.
- A stamped ack is a floor vote from its slot and can raise the floor.

Without L3, a host could sign an ack naming any integer as `ref_nonce`. That ack would choose the floor it is judged against and still count as a vote.

## What the full history buys

Little. A host can already name any real heartbeat in the escrow's past, for example nonce 1, and be judged against that old floor. Once the floor index has dropped past its `DefaultFloorWindow` (4096) entries, L0 skips that ack altogether. An ack of a turn whose record is gone already contributes nothing to turn accounting.

The part of L3 that depends on exact membership is the recent window. That is where a forged `ref_nonce` could name a heartbeat in the future, or a nonce inside the live window that never held one.

---

## Proposed rule

Each tracker keeps a **cutoff**, `prunedBelow`: one past the last nonce of the newest turn record that `prune` has deleted.

- **L3, every replica:** an ack with `ref_nonce < prunedBelow` is accepted without a lookup. An ack with `ref_nonce >= prunedBelow` takes the exact `heartbeatAt` lookup, unchanged.
- **Prune:** when a turn record is deleted, delete its heartbeat nonces (`RequestSpan[0]..RequestSpan[1]`) from `heartbeatAt`, and raise `prunedBelow` to `RequestSpan[1] + 1`.
- **Compose, gateway only:** leave out acks with `ref_nonce < prunedBelow`. They are dropped from the pending queue like any other transaction best-effort compose rejects.
- **Attribution:** `forAck` returns 0 for a stale ack. `checkL7` skips such acks instead of grouping them under turn 0. Otherwise they would merge into the previous-turn bucket when `TurnBefore` returns 0. L4, L5a and L6 marks for them carry `TurnStart: 0`.

`heartbeatAt` is then bounded by `(DefaultTurnRetain + 1) × slots_num` entries.

### Why the cutoff is deterministic

`prune` runs only from `TurnTracker.Observe`, at the end of each applied diff. It removes the oldest turns by count. Both inputs come from the journal. Every replica that applied diffs 1..N holds the same turn set and the same `prunedBelow`. The gateway's own tracker folds the same diffs and reaches the same value.

### Why no valid diff becomes invalid

Turn spans never overlap: `turnStartFor` joins a heartbeat to the newest turn only when it falls inside that turn's span. So every heartbeat at or above the cutoff belongs to a retained turn, and its `heartbeatAt` entry is still present. At or above the cutoff, L3 gives the same answer as today. Below it, L3 accepts everything.

The new rule is therefore strictly more permissive than the old one, which keeps three things safe:

- **Journal replay:** recovery, `ApplyLocalPersisted` and `foldHeightSync` accept every diff the old binary accepted.
- **Old gateways:** a diff an old gateway composes passes new hosts.
- **New gateways:** a new gateway leaves stale acks out, so everything it composes passes old hosts.

The only diff that changes from invalid to valid carries an ack whose `ref_nonce` is below the cutoff and never held a heartbeat. No honest gateway composes it.

---

## State root and snapshot

**State root.** The root does not change. `ComputeRestHashV2` covers the balance, the sealed accumulator and live inferences, the warm keys, and `hashHeightSyncEscrow`. That last hash covers only the forced-turn flags. The turn tracker, `heartbeatAt` and the floor are outside it today.

`prunedBelow` is derived from the journal in the same way and stays outside it too. Hashing it would change the root of every diff and need a new `StateRootAndProtocolVersion`, while detecting nothing new. Two replicas that disagree on the cutoff have already applied different diffs. That shows up as an L3 accept/reject mismatch on the next diff that carries an ack.

**Snapshot.** The snapshot is unchanged. Restart rebuilds the tracker, cutoff included, by replaying the journal. When that replay fails, `foldHeightSync` seeds the tracker with `SeedCompleted`, which gives an empty `heartbeatAt` and `prunedBelow = 0`. That replica rejects acks of earlier heartbeats, exactly as it does today.

Storing `prunedBelow` alone would not help there, because the entries for the retained window are missing too. Putting the whole tracker in the snapshot is a separate change; see Follow-up.

---

## Rollout

This is a validity change without a root change. Replicas of one escrow must agree on which diffs they accept, so the cap ships in a coordinated release:

- Add the turn retention to the `FleetCompat` token, so versiond refuses blue/green overlap between old and new binaries on one host:

  ```text
  d_ack=<n>,f=<ms>,lease=instance_id,turn_retain=64
  ```

- `DefaultTurnRetain` becomes a protocol constant. Today it only bounds memory and the L7 tail. With the cap it decides where L3 stops looking, so changing it later needs the same coordination.

**Mixed-version window.** A malicious user can compose a diff with a fabricated stale ack. New hosts accept it and old hosts reject it, so the two diverge on that escrow. Honest gateways never compose such a diff. The coordinated upgrade closes the window.

---

## Implementation

1. **`heightsync/turn.go`**
   - Add `prunedBelow uint64` to `TurnTracker`.
   - In `prune`, for each deleted turn: delete `RequestSpan[0]..RequestSpan[1]` from `heartbeatAt` and raise `prunedBelow`.
   - Copy `prunedBelow` in `Clone`.
   - Add `StaleRef(nonce uint64) bool`, which returns `nonce < prunedBelow`.
   - Rewrite the comment in `prune` and the `DefaultTurnRetain` doc, which currently say `heartbeatAt` is kept for the session.
2. **`heightsync/logplane.go`**
   - In `checkL3`, accept the ack when `st.Tracker.StaleRef(ack.RefNonce)` is true.
   - In `checkL7`, skip acks whose `forAck` result is 0.
3. **`state/machine.go`, `localBestEffortLocked`**
   - Before `logPlaneErrLocked`, drop an ack whose `ref_nonce` is stale, and log it at debug level through `logDroppedTx`.
   - `retainPendingLocked` keeps only held transactions, so the ack also leaves the gateway queue.
   - Host-side checking goes through `applyCore`, which never reaches this filter.
4. **Optional, host side:** stop admitting stale acks to the host mempool (`ingestRepairHeight`) and stop proposing them, so a host does not resend them on every exchange.
5. **`heightsync/params.go`:** extend `FleetCompat`.

## Tests

- **Rewrite:**
  - `TestTurnTracker_PrunesCompletedTurns`: `HeartbeatAtCount() <= (DefaultTurnRetain+1) × slots`. `prunedBelow` equals one past the last pruned span.
  - `TestLogPlane_LateAckAfterTurnPruneAccepted`: the late ack still passes, now through `StaleRef`.
- **Add:**
  - Inside the retained window, an ack naming a nonce without a heartbeat is still `ErrAckCausality`.
  - Below the cutoff, an ack naming a nonce without a heartbeat is accepted, changes no turn record, and is skipped by L7.
  - Gateway compose drops a stale ack, and it is gone from `pendingTxs`.
  - A journal written under today's rule, including a real stale ack, replays through `ApplyLocalPersisted` and `foldHeightSync` with identical post-state roots.
  - Two trackers fed the same journal end with the same `prunedBelow` and `heartbeatAt`.
  - `Clone` carries the cutoff, and a trial apply that prunes does not leak into the live tracker after `restoreMutable`.

---

## Cost

| | Today, 30 days | With the cap |
| --- | --- | --- |
| Group of 8 | 864,000 entries, about 17 MB | at most 520 entries, about 10 KB |
| Group of 32 | 3.5 million entries, about 70 MB | at most 2,080 entries, about 40 KB |

The per-diff clone shrinks by the same ratio on every host and on the gateway.

The code change is small: about 20 lines in `turn.go`, 10 in `logplane.go` and 10 in `machine.go`, plus tests.

What the cap gives up:

- **Coordinated release.** It is a validity change. See Rollout.
- **Stale acks no longer land from new gateways.** These are acks of a heartbeat more than 64 turns old, at least about 25 minutes at the default cadence. They no longer feed the floor or credit gateway cadence. Their height is old by then, so a floor raise from one is unlikely. They already do nothing for turn accounting.
- **Thinner attribution.** A mark on a stale ack names turn 0 instead of the original turn start.
- **Unchanged.** A replica whose journal replay failed keeps an empty index and rejects acks of earlier heartbeats, as it does today.

---

## Follow-up

With the cap, the whole tracker is at most 65 turn records and `(64+1) × slots_num` index entries. That is small enough to put in the snapshot envelope next to the floor blob. Restart could then replay from the snapshot nonce instead of nonce 1, and a replica whose replay failed would keep a usable L3 window.

That needs the floor blob's rule: install the tracker only when it is a consistent fold of 1..snapshot nonce. It is out of scope here.
