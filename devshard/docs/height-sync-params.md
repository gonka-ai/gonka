# Height-sync parameters

**Spec:** [`proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) §20  
**Code:** `devshard/heightsync/params.go` (`HeartbeatConfig`, `RepairConfig`)  
**Plan:** [`height-sync-implementation-plan.md`](./height-sync-implementation-plan.md) §8.4  
**Tests:** H25 in [`height-sync-tests.md`](./height-sync-tests.md)

These knobs do not change inference traffic. They only govern how a quiet escrow
keeps proving that participants still agree on mainnet height.

`K` (nonce cadence of Anchors) is a different knob; see
[Related transport knobs](#related-transport-knobs).

## The split: schedule in milliseconds, judge in blocks

Every knob here belongs to exactly one of two jobs, and the unit follows the job.

**Scheduling** asks *"is it time to act?"* That needs a `now`, and the only `now`
either party actually has is its own wall clock. Mainnet height is the **result**
of a height-sync turnover, so a block-denominated schedule is circular: a quiet
user cannot notice that a block passed until it syncs, and syncing is the thing
the schedule was supposed to trigger. A silent host is in the same position — it
learns height from traffic, so a partitioned host sees a frozen tip and would
never count enough blocks to arm. Scheduling is therefore in **milliseconds**:
`Interval`, `TurnTimeout`, `IdleTimeout`.

**Evaluation** asks *"was this ack late? did this turn complete?"* Both sides of
those comparisons are claims already sitting in `Diff`, so every replaying
verifier recomputes the same verdict with no clock at all. Evaluation stays in
**blocks**: `AckDeadlineBlocks`, `DeltaBlocks`.

The invariant that makes this safe: **no wall clock ever enters `Diff` or a
`SyncTurnRecord`.** The cadence lives in `heightsync.Heartbeat` (producer-local)
and the arming timer in `heightsync.CloseReady` (host-local). Neither is folded
into the log, so turn state remains a pure function of `Diff` and independent
verifiers never have to agree on a clock. Arming is a local liveness decision
that emits nothing; finalization is where hosts must actually agree, and it
aggregates per-host evidence rather than trusting one host's timer.

### A turnover, not a round count

The obligation is *"a full height-sync round-trip must land at least every
`Interval`"*. A **turnover** is `Q` distinct host-signed height claims:

- `MsgHeightAck` from a heartbeat turn, or
- an executor-stamped response riding ordinary Anchor traffic (E7).

Either discharges the obligation, so a busy escrow emits **zero** heartbeats and
a quiet one emits exactly as many as it needs. What does *not* count is the
user's own stamp on `MsgStartInference`: it is self-signed and proves nothing
about what any host saw. `ORACLE_UNAVAILABLE` acks do not count either — they
are required, but they carry no height. This is the same rule `TurnTracker` uses
for `Q`, so the scheduler and the record agree on what "counted".

`MinRoundsPerBlock` is gone. It existed to force a second nonce round so acks
could land inside the same block; with a wall clock the producer simply keeps
composing ack-carrying diffs until the turn turns over. One round is always owed
(the request span awaited no ack), and further rounds are driven by acks
actually arriving, bounded by `slots_num + 1` because each slot acks once.

`TurnTimeout` replaces the part `MinRoundsPerBlock` never covered: how long to
wait on a turn that never reaches quorum. Without it, one unreachable slot would
leave a turn open forever and silence the cadence for the rest of the session. A
turn that the log has already settled as `degraded` releases the producer
immediately, without burning the rest of `TurnTimeout`.

`D_ack = 1` remains the verifier window: an ack is on time while
`observed_height ≤ h_req + D_ack`. A block tick during transport does not
degrade the turn if the host still stamped `H` (or `H+1`). Ingest height of the
Diff is not the lateness clock.

---

## How values are chosen

1. **Compiled defaults** in `DefaultHeartbeatConfig` / `DefaultRepairConfig`.
2. **Optional overlay** from the runtime-config snapshot (`Snapshot.HeightSync`).
   Only **non-zero** snapshot fields replace a default. Inference-chain does not
   publish these yet, so production snapshots are all zeros and both host and
   user get the compiled defaults.
3. `runtimeparams.SessionParams().Heartbeat` / `.Repair` rebuild from the live
   snapshot on every call (`HeartbeatConfigFromSnapshot`,
   `RepairConfigFromSnapshot`). Host and user are meant to read the same
   numbers from this provider (E2/E3 wiring).

Zero on the wire always means “keep the compiled default”, never “disable”.
`D_ack`’s compiled default is `1`; a snapshot value of `2` is extra slack and
must still satisfy `T_idle > Interval + TurnTimeout`.

Overriding `IntervalMs` alone also moves `TurnTimeout` and `IdleTimeout`, which
default to `Interval` and `4 · Interval`. Without that, raising the interval
would leave an absolute `T_idle` behind and `Validate` would reject a config the
operator had every reason to think was fine.

`HeartbeatConfig.Validate(freshness)` checks the resolved config and no longer
takes a block time — nothing left in the schedule needs converting out of
blocks. Note that today it is only exercised by H25, not on the live overlay
path: `SessionParams()` rebuilds from a long-poll snapshot and has no error
channel. An `IdleTimeoutMs` override below `Interval + TurnTimeout` therefore
takes effect unchecked and makes hosts arm mid-turn. Rejecting or clamping a bad
overlay in `HeartbeatConfigFromSnapshot` is an open follow-up (plan §8.4).

---

## Log-plane cadence (`HeartbeatConfig`)

Scheduling — local wall clock, never in `Diff`:

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `Interval` | `Interval` | `3s` | Longest gap between full height-sync turnovers. The producer opens a heartbeat turn when no turnover has landed within it. |
| `TurnTimeout` | `TurnTimeout` | `= Interval` (`3s`) | How long the producer waits on one open turn before abandoning it and opening a fresh one. Stops a single unreachable slot from stalling the cadence. |
| `T_idle` | `IdleTimeout` | `4 · Interval` (`12s`) | How long a **host** may see no user contact before it arms close-ready and prepares to treat the sequencer as failed. Reason is **silence only** — a missing ack never arms. |

Evaluation — logged heights, deterministic under replay:

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `D_ack` | `AckDeadlineBlocks` | `1` | Stamp slack after `h_req`. An ack is late iff `observed_height > h_req + D_ack`. The turn **degrades** when that window has closed and counting acks `< Q`. Missing acks are not fraud. |
| `D` | `DeltaBlocks` | `2` | How far a host’s oracle tip may sit from the heartbeat’s `h_ref` and still report `SYNCED`. Farther → `CATCHING_UP`. Strong escalation on that value is Phase F; E only reports it. |

`DeltaBlocks` is **not** on the snapshot; it stays the compiled default until
Strong / `StrongPolicy.D` lands.

### Timeline (defaults)

```
turnover          due: open turn        give up on turn            arm (if still silent)
   |                  |                       |                            |
   |←— Interval 3s —→|                        |                            |
   |                  |←— TurnTimeout 3s —→|                               |
   |←———————————————— T_idle = 4 × Interval = 12s ——————————————————————→|
  t=0                t=3s                   t=6s                        t=12s
```

At `t = 3s` the user opens the request span and keeps composing ack-carrying
diffs until `Q` acks land — no waiting for a block. One lost cycle occupies
`Interval + TurnTimeout = 6s`, which is why `T_idle` must be strictly larger:
a host must never arm on a single missed turnover. With the shipped `4 ×`
multiple a host tolerates two lost cycles before it arms.

### Constraints (`Validate`)

| Rule | Why |
| ---- | --- |
| `T_idle > Interval + TurnTimeout` | One lost turnover must not arm a host. |
| `2 · Interval ≤ F` | Two turnovers must fit inside freshness `F` (default 60s), so a height claim does not go stale between them. |

`Validate` uses `DefaultOriginatorFreshness` (`F` = 60s) when the argument is
zero. Shipped defaults pass: `12s > 3s + 3s` and `2 · 3s = 6s ≤ 60s`.

---

## Repair budget (`RepairConfig`)

Caps host→host traffic when an ack is absent
past `D_ack`. Probes fetch a height; they never assign blame. Wired in E5.

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `δ_probe` | `Stagger` | `1s` | Delay before host `V` probes missing slot `j`: `((V_slot − j) mod slots_num) · δ_probe`. Late probers often see the ack already in `Diff` and skip. |
| `R_max` | `MaxProbesPerWindow` | `0` → use `slots_num` | Cap on probes per host per `Interval` of wall time. A prober that is missing acks is by definition learning no heights, so only elapsed time can refill its budget. Stops a dead slot from becoming a flood. |

Snapshot fields: `ProbeStaggerMs`, `MaxProbesPerWindow`. Zero = compiled default.

---

## Snapshot overlay (`HeightSyncParams`)

Carried on NodeManager `RuntimeConfig` fields 13–18. Mapped 1:1 onto
`Snapshot.HeightSync`.

| Proto field | Snapshot field | Overlays |
| ----------- | -------------- | -------- |
| `height_sync_interval_ms` (13) | `IntervalMs` | `Interval` |
| `height_sync_ack_deadline_blocks` (14) | `AckDeadlineBlocks` | `D_ack` |
| `height_sync_idle_timeout_ms` (15) | `IdleTimeoutMs` | `T_idle` |
| `height_sync_probe_stagger_ms` (16) | `ProbeStaggerMs` | `δ_probe` |
| `height_sync_max_probes_per_window` (17) | `MaxProbesPerWindow` | `R_max` |
| `height_sync_turn_timeout_ms` (18) | `TurnTimeoutMs` | `TurnTimeout` |

Field numbers 13 and 15 kept their slots and changed name and unit. Inference
chain does not publish any of them yet, so no deployed sender is affected.

`DeltaBlocks` has no proto field. Snapshot overlay never changes it.

Testenv can override in-process via `SetSnapshot` with these fields set.

---

## Related transport knobs

These are **not** `HeartbeatConfig` fields. They already exist on the transport
plane (Phases A–D) and the log plane reuses them.

| Spec | Default | What it regulates |
| ---- | ------- | ----------------- |
| `K` | `8` (scheduler; testenv often `10`) | **Nonce** cadence of Anchor envelopes. Independent of the heartbeat `Interval`. |
| `slots_num` | escrow group size | Width of every turn (cadence, forced, heartbeat). `executor(n) = n mod slots_num`, so any consecutive span of this length addresses every slot once. |
| `Q` | `ceil(2/3 × N_hosts)` | Quorum for `(C-quorum)` **and** `(C-turn)`. There is no second quorum knob. `ORACLE_UNAVAILABLE` acks do not count. |
| `F` | `60s` | Originator freshness. Carry-forward older than `F` is `stale_origin`. Also the window used by `peer_seen` bit expiry. |
| `W_conf` | `256` heights | Confirmation-index window: only heights in `[tip − W_conf, tip]` count toward `(C-quorum)`. |
| `StaleAfter` | `10s` (oracle client) | Quiet oracle with a cached tip → `ORACLE_STALE` / degraded Anchor, not `ORACLE_UNAVAILABLE`. |

---

## Wire fields that are not parameters

A **sync vector** (`MsgHeartbeat.sync_vector`) is not a config knob. It is a
per-slot status array the user signs: one `SyncVectorEntry` per host, reporting
turn `turn_seq − 1` (`ACKED` / `MISSING` / `UNREACHABLE` / `REJECTED`). See
`devshard/heightsync/syncvector.go`. The log is authoritative; the vector is
early visibility. The only attributable lie is `ACKED` when `Diff` has no such
ack.

`MsgHeightAck.sync_state` is the host’s self-report (`SYNCED`, `CATCHING_UP`,
`ORACLE_STALE`, `ORACLE_UNAVAILABLE`), evaluated with `DeltaBlocks` and the
local oracle. `peer_seen` is a bitmap of slots this host holds a claim for,
fresh within `F`.
