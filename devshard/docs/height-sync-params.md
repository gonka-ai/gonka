# Height-sync parameters

**Spec:** [`proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) §20  
**Code:** `devshard/heightsync/params.go` (`HeartbeatConfig`, `RepairConfig`)  
**Plan:** [`height-sync-implementation-plan.md`](./height-sync-implementation-plan.md) §8.4  
**Tests:** H25 in [`height-sync-tests.md`](./height-sync-tests.md)

These knobs do not change inference traffic. They only govern how a quiet
escrow **after the first host-seeded `F`** (spec §10.3.1) keeps proving that
participants still agree on mainnet height. A session that has never inferred
does not heartbeat; hosts arm on `T_idle`.

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

One knob straddles the split, and `BlockTime` is why it can. `D_ack` answers a
scheduling question — *did this answer arrive while the request still stood?* —
using the log's only clock, which is height. The two are the same budget in
different units, so `D_ack` is **derived** from the schedule through an assumed
block time rather than shipped as a bare constant, and `Validate` holds the
conversion to account. `BlockTime` is the one deployment fact in the file; it is
not itself a policy.

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
a quiet one (after the first host-seeded `F`, spec §10.3.1) emits exactly as
many as it needs. Heartbeats do not start until that init. What does *not* count is the
user's own stamp on `MsgStartInference`: it is self-signed and proves nothing
about what any host saw. An `ORACLE_UNAVAILABLE` ack *does* count: with `(C-turn)`
withdrawn, completion certifies that `Q` slots were reachable and applying the
log, which a host with a dead follower proves exactly as well as a `SYNCED` one.
Excluding it made such a host a permanent hole in the roster's cadence, while
including it confirms nothing about heights — it echoes `F(m)`, and a carried
claim adds no new height to the log (§8.7.1). This is the same rule `TurnTracker` uses
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

It defaults to `2 · Interval`, not `Interval`. Equality left a turn no patience
at all: the span is dispatched slot by slot and only then waits for acks, so the
turn was abandoned at the exact moment the next became due, and a session whose
round trips ran past one interval reopened forever and recorded no turnover at
all. The failure was total rather than degraded, which is why this needs real
headroom.

### The ack window is the schedule in blocks

An ack is on time while `observed_height ≤ h_req + D_ack`, where `h_req` is the
height the request span was composed at and `observed_height` is the height the
**host** read when it composed its answer. That comparison is deliberate: the
ack's stamp is the one timestamp in the log the sequencer can neither forge nor
backdate, which is what makes withheld-and-drip-fed acks (attack 22) visible.

What must fit inside the window is the whole turn, not one message in flight.
Heartbeats for a turn are composed together at a single height, but the acks
answering them are stamped as each host is reached, so their heights climb across
the span. The producer's own patience bounds that: it stops counting acks for a
turn after `Interval + TurnTimeout` (the **turnover budget**, the same quantity
`Heartbeat.Deadline` reports and `T_idle` must exceed). So

```
D_ack = ceil((Interval + TurnTimeout) / BlockTime) + 1
      = ceil(18s / 1s) + 1 = 19 blocks
```

The trailing block is the boundary: `h_req` is read at an arbitrary point inside
a block, so an elapsed span of `n` block times can cross `n + 1` boundaries.

The shipped `D_ack = 1` was the mismatch this replaces. It was justified as slack
for "a height change in flight", but the span plus an ack round trip exceeds one
block at every block time we ship, so honest acks were flagged `late` by
construction, turns degraded in steady state, repair probes fired against
nobody's fault, and `h_last` stopped advancing. The alternative — deriving the
window from `slots_num` — was refused for the same reason step 1 refused it for
`D`: a tolerance must not be coupled to a topology number. Bounding it by the
producer's own patience needs no roster size.

Ingest height of the Diff is not the lateness clock; only the ack's own stamp is.

---

## How values are chosen

1. **Compiled defaults** in `DefaultHeartbeatConfig` / `DefaultRepairConfig`.
   The shipped `Interval` is `6s`. `TurnTimeout` (`12s`) and `T_idle` (`24s`)
   are `2 ·` and `4 ·` that interval; `D_ack` (`19`) is the 18s turnover budget
   expressed in 1s blocks plus the boundary block. Overlaying `IntervalMs`
   alone moves the two timeouts with it; `D_ack` stays at the compiled 19.
2. **Optional overlay** from the runtime-config snapshot (`Snapshot.HeightSync`).
   Only **non-zero** snapshot fields replace a default. Inference-chain does not
   publish these yet, so production snapshots are all zeros and both host and
   user get the compiled defaults.
3. `runtimeparams.SessionParams().Heartbeat` / `.Repair` rebuild from the live
   snapshot on every call (`HeartbeatConfigFromSnapshot`,
   `RepairConfigFromSnapshot`). Host and user read those numbers at session
   construction (`HostManager.SetParamsProvider`, `HTTPSessionConfig.Heartbeat`).

Zero on the wire always means “keep the compiled default”, never “disable”.

Overriding `IntervalMs` alone also moves `TurnTimeout` and `IdleTimeout`, which
are derived from `Interval` (`2 ·` and `4 ·`). Evaluation knobs do **not**
follow the overlay: `AckDeadlineBlocks`, `BlockTime`, and `DeltaBlocks` stay
compiled, because they feed `SyncTurnRecord` and L0.
`HeartbeatConfigFromSnapshot` overlays only the scheduling fields, then
`Validate`s against those compiled knobs. An overlay that would fail — a
schedule whose turnover budget no longer fits the compiled ack window, or
whose `T_idle` is not strictly larger — is clamped back to compiled defaults
and counted (`OverlayClampCount`). Zero on the wire still means “keep the
compiled default”, never “disable”.

`HeartbeatConfig.Validate(freshness)` checks the resolved config and no longer
takes a block time — nothing left in the schedule needs converting out of
blocks. The live overlay path calls it on every snapshot (H25, H63).

---

## Log-plane cadence (`HeartbeatConfig`)

Scheduling — local wall clock, never in `Diff`:

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `Interval` | `Interval` | `6s` | Longest gap between full height-sync turnovers. The producer opens a heartbeat turn when no turnover has landed within it. |
| `TurnTimeout` | `TurnTimeout` | `2 · Interval` (`12s`) | How long the producer waits on one open turn before abandoning it and opening a fresh one. Stops a single unreachable slot from stalling the cadence, and gives the span plus its acks room to land. |
| `T_idle` | `IdleTimeout` | `4 · Interval` (`24s`) | How long a **host** may see no user contact before it arms close-ready and prepares to treat the sequencer as failed. Reason is **silence only** — a missing ack never arms. |
| `block_time` | `BlockTime` | `1s` | Assumed chain block interval — the rate that converts the schedule into `D_ack`. Not a policy: the default is the fastest chain we ship against (mock-dapi), which is the safe direction, since a window that is too wide only delays noticing a stalled turn while one too narrow calls honest acks late. |

Evaluation — logged heights, deterministic under replay:

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `D_ack` | `AckDeadlineBlocks` | derived: `19` | The turn's ack window after `h_req`. An ack is late iff `observed_height > h_req + D_ack`; the turn **degrades** when the window has closed and counting acks `< Q`, and only then is a repair probe due. Derived from `Interval + TurnTimeout` through `BlockTime` so the log never disowns a turn its own producer is still working on. Missing acks are not fraud. |
| `D` | `DeltaBlocks` | `2` | How far a host’s oracle tip may sit from the heartbeat’s `h_ref` and still report `SYNCED`. Farther → `CATCHING_UP`. Strong escalation on that value is Phase F; E only reports it. |
`W_conf` used to be here as `HeartbeatConfig.WindowBlocks`, bounding how far one
signer could raise `F` and how far above its own tip a producer would carry it.
Both bounds are **removed** and the field no longer exists (spec §14 *Why no
`W_conf` on the floor*). The height a stamp carries was already bounded where it
entered the system — the envelope, where `|Δ| > D` demands Strong — so a second
distance test on the log bought nothing, while its failure mode was real: a
first host that poisons `F` at height `1` leaves every later honest host, now at
`10 000`, unable to raise the floor and unwilling to carry it. `W_conf` survives
only as the confirmation-index window in §17, which is not compiled into
`HeartbeatConfig`.

`DeltaBlocks` is not on the snapshot; it stays the compiled default, waiting on
Strong / `StrongPolicy.D`. It gates a consensus check (envelope admission), so
changing it mid-session would make two verifiers disagree about the same
envelope and it needs a coordinated rollout rather than a long-poll overlay.

### Timeline (defaults)

```
turnover          due: open turn                 give up on turn      arm (if still silent)
   |                  |                                |                     |
   |←— Interval 6s —→|                                 |                     |
   |                  |←———— TurnTimeout 12s ————→|                           |
   |←—————— turnover budget = 18s ——————————————→|                           |
   |←—————— ack window = D_ack · block_time = 19s ————————→|                 |
   |←———————————————— T_idle = 4 × Interval = 24s —————————————————————————→|
  t=0                t=6s                            t=18s                 t=24s
```

At `t = 6s` the user opens the request span and keeps composing ack-carrying
diffs until `Q` acks land — no waiting for a block. One lost cycle occupies the
turnover budget of `18s`, which is why `T_idle` must be strictly larger: a host
must never arm on a single missed turnover.

The ack window sits between the two: at least as long as the producer's own
patience, so the log stops waiting just *after* the producer does, and shorter
than `T_idle`, so a host is never the last to know.

### Constraints (`Validate`)

| Rule | Why |
| ---- | --- |
| `D_ack · block_time ≥ Interval + TurnTimeout` | The log must not declare a turn degraded while its producer is still legitimately collecting the acks it asked for. |
| `T_idle > Interval + TurnTimeout` | One lost turnover must not arm a host. |
| `2 · Interval ≤ F` | Two turnovers must fit inside freshness `F` (default 60s), so a height claim does not go stale between them. |

The first two read as one chain: the log waits at least as long as the producer,
and the host waits longer than either.

`Validate` uses `DefaultOriginatorFreshness` (`F` = 60s) when the argument is
zero. Shipped defaults pass: `19 · 1s ≥ 18s`, `24s > 18s`, and `2 · 6s ≤ 60s`.
A deployment that sets `AckDeadlineBlocks` by hand without saying what its blocks
are is exactly what the first rule catches — `D_ack = 2` on the shipped schedule
is now rejected, and the same value passes once `block_time` is `10s`.

---

## Repair budget (`RepairConfig`)

Caps host→host traffic when an ack is absent
past `D_ack`. Probes fetch a height; they never assign blame. Wired in E5.

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `δ_probe` | `Stagger` | `1s` | Delay before host `V` probes missing slot `j`: `((V_slot − j) mod slots_num) · δ_probe`. Late probers often see the ack already in `Diff` and skip. |
| `R_max` | `MaxProbesPerWindow` | `0` → use `slots_num` | Cap on probes **and** responder HEIGHT builds per host per `Interval` of wall time. A prober that is missing acks is by definition learning no heights, so only elapsed time can refill its budget. The same window bounds a peer flooding the repair endpoint. |

Snapshot fields: `ProbeStaggerMs`, `MaxProbesPerWindow`. Zero = compiled default.

---

## Snapshot overlay (`HeightSyncParams`)

Carried on NodeManager `RuntimeConfig` fields 13–19. Mapped 1:1 onto
`Snapshot.HeightSync`.

| Proto field | Snapshot field | Overlays |
| ----------- | -------------- | -------- |
| `height_sync_interval_ms` (13) | `IntervalMs` | `Interval` |
| `height_sync_ack_deadline_blocks` (14) | `AckDeadlineBlocks` | **ignored** — `D_ack` stays compiled |
| `height_sync_idle_timeout_ms` (15) | `IdleTimeoutMs` | `T_idle` |
| `height_sync_probe_stagger_ms` (16) | `ProbeStaggerMs` | `δ_probe` |
| `height_sync_max_probes_per_window` (17) | `MaxProbesPerWindow` | `R_max` |
| `height_sync_turn_timeout_ms` (18) | `TurnTimeoutMs` | `TurnTimeout` |
| `height_sync_block_time_ms` (19) | `BlockTimeMs` | **ignored** — `block_time` stays compiled |

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
| `Q` | `ceil(2/3 × N_hosts)` | Quorum for **turn completion** only. The floor takes no vote — any single host-signed claim sets it (spec §14). Envelope `(C-quorum)` / `IsStrictlyConfirmed` is **withdrawn** (spec §17); consumers use local oracle readiness. |
| `F` | `60s` | Originator freshness. Carry-forward older than `F` is `stale_origin`. Also the window used by `peer_seen` bit expiry. |
| `W_conf` | `256` heights | Confirmation-index window (`[tip − W_conf, tip]`) and nothing else. No longer a floor or producer bound. |
| `StaleAfter` | `10s` (oracle client) | Quiet oracle with a cached tip → `ORACLE_STALE` / degraded Anchor, not `ORACLE_UNAVAILABLE`. |

---

## Wire fields that are not parameters

A **sync vector** (`MsgHeartbeat.sync_vector`) is not a config knob. It is a
per-slot status array the user signs: one `SyncVectorEntry` per host, reporting
the preceding turn (`ACKED` / `MISSING` / `UNREACHABLE` / `REJECTED`). See
`devshard/heightsync/syncvector.go`. The log is authoritative; the vector is
early visibility. The only attributable lie is `ACKED` when `Diff` has no such
ack.

`MsgHeightAck.sync_state` is the host’s self-report (`SYNCED`, `CATCHING_UP`,
`ORACLE_STALE`, `ORACLE_UNAVAILABLE`), evaluated with `DeltaBlocks` and the
local oracle. `peer_seen` is a bitmap of slots this host holds a claim for,
fresh within `F`.
