# Release guide: `devshard-0.2.14-v4`

Placeholder for the v4 release notes, upgrade steps, and rollout checklist.

---

## Overview

<!-- TBD -->

---

## What's in this release

<!-- TBD -->

---

## Upgrade / rollout

<!-- TBD -->

---

## Compatibility

### Gateway status: `protocol_version` → `session_version`

The gateway runtime status field **`protocol_version`** (always `"1"` from the
removed client routing enum) is replaced by **`session_version`**.

| Before | After |
|--------|-------|
| `protocol_version: "1"` | `session_version: "v2"` (example) |

**`session_version`** is the session bind tag from
`EscrowState.StateRootAndProtocolVersion` — the same value used for state-root
hashing and on-chain settlement (`state_root_and_protocol_version`). That is
what external tools should display or assert on, not the old `"1"` enum.

**Migration:** update any monitor, dashboard, or script that polled gateway
`/status` (or equivalent) for `protocol_version` to read **`session_version`**
instead.

Gateway devshard config/API: the old **`protocol_version`** request field is
**`route_prefix`** (e.g. `/devshard/v2`). The legacy HTTP mount
`/v1/devshard` is no longer supported; clients must use `/devshard/<version>/`.

<!-- TBD -->

### Storage backend: Postgres required for HA / multi-instance

> **Note:** To run a **highly-available (HA)** devshard deployment — i.e. more
> than one `versiond` / `devshardd` instance serving the same network — you
> **must** use the **Postgres** storage backend. Always set `PGHOST` (and the
> related `PG*` connection env) so each instance selects Postgres at boot.

Why: cross-instance coordination (the validation **lease** table that ensures
only one instance validates each `(escrow_id, inference_id)` pair) only works on
a **shared** store. The **SQLite** backend is **single-instance by
construction** — it is a single-writer file, and its lease store is now a no-op
(`Acquire` always grants, `AcquireOneStale`/`SetResult` do nothing). Running two
instances against SQLite gives **no dedup** and risks data corruption on a
shared data dir.

Rule of thumb:

| Deployment | Backend |
|------------|---------|
| Single instance / local dev / Testermint | SQLite **or** Postgres |
| **Multiple `versiond` instances (HA)** | **Postgres (required)** |

See `devshard/docs/storage-design.md` (storage-mode selection) and
`devshard/docs/rolling-update.md` ("multi-instance ⇒ Postgres").

---

## Known follow-ups

### Escrow ID: `string` vs `uint64`

On this branch, devshard keeps **`escrowID` as `string`** in the bridge API,
session storage keys, HTTP paths, and devshard protocol messages. On-chain
`DevshardEscrow.id` remains **`uint64`**; adapters (e.g. `cmd/devshardd/bridge`)
parse/format at the chain boundary.

Moving to **`uint64` throughout devshard** is reasonable and aligns with chain
types, but it is a **protocol / wire contract change** (bridge interface, session
participants, and possibly protos), not a standalone hygiene fix.

**Recommendation:** treat escrow ID type unification as a **low-priority**
change. Bundle it with **more important protocol changes** (state-root version
bumps, settlement wire changes, etc.) so operators upgrade once, not for a
cosmetic type change alone.

Until then, keep **string** escrow IDs in devshard and **uint64** only at
chain I/O.
