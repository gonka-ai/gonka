# Height-sync parameters

**Spec:** [`proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) §20  
**Code:** `devshard/heightsync/params.go` (`HeartbeatConfig`, `RepairConfig`)  
**Plan:** [`height-sync-implementation-plan.md`](./height-sync-implementation-plan.md) §8.4  
**Tests:** H25 in [`height-sync-tests.md`](./height-sync-tests.md)

These knobs do not change inference traffic. They only govern how a quiet escrow
keeps proving that participants still agree on mainnet height.

Heights below are **mainnet blocks**, not wall-clock and not session nonces.
`K` (nonce cadence of Anchors) is a different knob; see
[Related transport knobs](#related-transport-knobs).

### Why `K_hb` is in blocks, not milliseconds

A millisecond cadence would be a **local clock**. Two hosts (and any later
verifier replaying `Diff`) do not share a clock, so they cannot agree on
whether a heartbeat was due, whether an ack was late, or whether to arm
close-ready. Height-sync exists so those decisions key on a value every honest
party can recompute: mainnet height.

`IntervalBlocks` therefore cannot be a duration. Snapshot overlay and
`Validate` are what make an override **validatable by other hosts** — they
read the same `HeartbeatConfig` from the runtime-config snapshot (or the same
compiled default). A per-process `time.Ticker` would not be in that snapshot
and would not appear in `Diff`.

### Two nonce rounds per block (near-zero divergence)

Height itself still ticks once per block. “Twice per block” is **not** `D_ack = 0`.
It is a user-scheduler target, `MinRoundsPerBlock = 2`:

1. **Request round** — `slots_num` consecutive `MsgHeartbeat` diffs (no ack wait).
2. **Response round** — later diffs, still aiming at the same block, that append `MsgHeightAck`.

`K_hb = 1` opens one *cycle* per new block. E2 must keep composing nonce rounds
until that floor is met, without waiting for height `H+1`. Hosts cannot punish
“only one round”; they only see whether enough acks exist. So
`MinRoundsPerBlock` is **compiled-only** — not a snapshot overlay.

`D_ack = 1` is the verifier window: an ack is on time while
`observed_height ≤ h_req + D_ack`. A block tick during transport does not
degrade the turn if the host still stamped `H` (or `H+1`). Ingest height of
the Diff is not the lateness clock.

You cannot stamp two different heights in one block. For denser stamps while
the escrow is busy, use `observed_height` on inference txs (E7).

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
must still satisfy `T_idle > K_hb + D_ack`. `MinRoundsPerBlock` is not on the
wire.

`HeartbeatConfig.Validate(blockTime, freshness)` runs against the resolved
config. A bad override must fail fast (H25), not silently arm hosts.

---

## Log-plane cadence (`HeartbeatConfig`)

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `K_hb` | `IntervalBlocks` | `1` | How long the user may stay quiet before it **must** open a heartbeat cycle. Due when `h_now − h_last ≥ K_hb` (every new block). |
| — | `MinRoundsPerBlock` | `2` (compiled only) | E2 scheduler: at least two nonce rounds per cycle (request, then ack-carrying diffs). Not a verifier deadline and not on the snapshot. |
| `D_ack` | `AckDeadlineBlocks` | `1` | Stamp slack after `h_req`. An ack is late iff `observed_height > h_req + D_ack`. The turn **degrades** when that window has closed and counting acks `< Q`. Missing acks are not fraud. |
| `T_idle` | `IdleBlocks` | `3` (`3 · K_hb`) | How long a **host** may see no user contact before it arms close-ready. Reason is **silence only** — a missing ack never arms. |
| `D` | `DeltaBlocks` | `2` | How far a host’s oracle tip may sit from the heartbeat’s `h_ref` and still report `SYNCED`. Farther → `CATCHING_UP`. Strong escalation on that value is Phase F; E only reports it. |

`DeltaBlocks` and `MinRoundsPerBlock` are **not** on the snapshot. They stay
the compiled defaults (`DeltaBlocks` until Strong / `StrongPolicy.D` lands).

### Timeline (defaults)

```
h_last     due (request + ack rounds)     stamp window closes          arm (if silent)
   |            |                              |                       |
   |← K_hb=1 →|  D_ack=1                       |                       |
   |←———————— T_idle = 3 —————————————————————————————————————————————→|
  100         101                            102                     103
```

At height 101 the user opens the request span and keeps composing diffs
(`MinRoundsPerBlock = 2`) so acks can land without waiting for 102. An ack
that still stamps `≤ 102` counts. One lost cycle occupies `K_hb + D_ack = 2`
blocks, which is why `T_idle` must be strictly larger.

### Constraints (`Validate`)

| Rule | Why |
| ---- | --- |
| `MinRoundsPerBlock ≥ 2` | The cycle is request + ack-carrying round(s). |
| `T_idle > K_hb + D_ack` | One lost heartbeat cycle must not arm a host. |
| `K_hb · block_time ≤ F / 2` | Two cycles must fit inside freshness `F` (default 60s), so a height claim does not go stale between turns. |

`Validate` uses `DefaultAssumedBlockTime` (6s) and `DefaultOriginatorFreshness`
(`F` = 60s) when those arguments are zero. Shipped defaults pass:
`1 · 6s = 6s ≤ 30s`, `2 ≥ 2`, and `3 > 1 + 1`.

---

## Repair budget (`RepairConfig`)

Caps host→host traffic when an ack is absent
past `D_ack`. Probes fetch a height; they never assign blame. Wired in E5.

| Spec | Go field | Default | What it regulates |
| ---- | -------- | ------- | ----------------- |
| `δ_probe` | `Stagger` | `1s` | Delay before host `V` probes missing slot `j`: `((V_slot − j) mod slots_num) · δ_probe`. Late probers often see the ack already in `Diff` and skip. |
| `R_max` | `MaxProbesPerWindow` | `0` → use `slots_num` | Cap on probes per host per `K_hb` window. Stops a dead slot from becoming a flood. |

Snapshot fields: `ProbeStaggerMs`, `MaxProbesPerWindow`. Zero = compiled default.

---

## Snapshot overlay (`HeightSyncParams`)

Carried on NodeManager `RuntimeConfig` fields 13–17. Mapped 1:1 onto
`Snapshot.HeightSync`.

| Proto field | Snapshot field | Overlays |
| ----------- | -------------- | -------- |
| `height_sync_interval_blocks` | `IntervalBlocks` | `K_hb` |
| `height_sync_ack_deadline_blocks` | `AckDeadlineBlocks` | `D_ack` |
| `height_sync_idle_blocks` | `IdleBlocks` | `T_idle` |
| `height_sync_probe_stagger_ms` | `ProbeStaggerMs` | `δ_probe` |
| `height_sync_max_probes_per_window` | `MaxProbesPerWindow` | `R_max` |

`MinRoundsPerBlock` has no proto field. Snapshot overlay never changes it.

Testenv can override in-process via `SetSnapshot` with these fields set.

---

## Related transport knobs

These are **not** `HeartbeatConfig` fields. They already exist on the transport
plane (Phases A–D) and the log plane reuses them.

| Spec | Default | What it regulates |
| ---- | ------- | ----------------- |
| `K` | `8` (scheduler; testenv often `10`) | **Nonce** cadence of Anchor envelopes. Independent of `K_hb`. |
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
