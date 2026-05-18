# Height-sync — open questions (development notes)

Parking lot for unresolved design choices. Normative behaviour lives in
[`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](../docs/proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md);
this file is **not** part of the production spec.

---

## Tuning and policy

### `K` / `slots_num` / `D` per-deployment tuning

Currently defaults match testenv (`K=8`, `slots=4`, `D=2`). Production
rosters with ≥ 10 hosts may want larger `K`.

### Strong recency policy

`max_lag_blocks` is currently `0` (recency check disabled). Should it
scale with mainnet block time and follower lag SLO?

---

## Strong mode and verification

### Validator-set rotation (Step 3b)

Whether to ship a real per-epoch resolver in this protocol, or to leave
it to
[`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](../docs/proposals/FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)
and use a `NoopEpochResolver` here.

### CometBFT decoder swap

Wire field 9 is `bytes`. Current carrier is `blockoracle.Header`. A
future switch to canonical `tendermint.types.LightBlock` is a
content-only migration but requires coordinated upgrade.

---

## Cadence and dispute

### `MsgForceHeightSyncTurn` reset semantics

Should opening a forced turn reset per-peer `last_propagated`?
Proposal-level open question; not blocking.

### Cross-session originator equivocation detection

Deduplication on `(sender_id, H)` across sessions; lives in the dispute
plan.
