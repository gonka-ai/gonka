# Release guide: `devshard-0.2.15-v5`

Operator-facing notes for the v5 line, building on the v4 HA / Postgres /
gRPC base. The headline change is **warm cutover**: a same-name SHA swap no
longer routes traffic at a child that is merely *ready* — it waits for the new
child's recovery backlog to drain first, so an overlap cutover lands on a host
that has already rebuilt its sealed-inference index and validation obs rather
than one mid-wipe.

Detailed deploy verification: [v5-deploy-test-plan.md](./v5-deploy-test-plan.md).
Rolling update mechanics: [rolling-update.md](./rolling-update.md).
Restore / snapshot work: [restore-host-loadsnapshot.md](./restore-host-loadsnapshot.md).
Sealed-inference index rebuild: [sealed-inference-index-rebuild-plan.md](./sealed-inference-index-rebuild-plan.md).

---

## Overview

v5 keeps the v4 HA topology (multi-`versiond` + `versiond-router` on shared
Postgres, sticky escrow routing, validation-lease exclusivity) and the gRPC
chain transport. What changes is the **recovery / readiness contract**:

- `devshardd` `/ready` is split into a **status code** (can this process serve:
  chain up, storage open, not draining) and a **body field** `recovery_complete`
  (is this process warm: backlog drained **and** background sealed-index /
  validation-obs repairs finished). Status is `200` within seconds of boot, so
  a solo restart is no longer force-stopped by the 60s `VERSIOND_READY_TIMEOUT`
  before it ever serves. The body field is the warm signal only a version
  replacement with a healthy old generation needs to wait on.
- `versiond`'s overlap swap waits for `recovery_complete: true` before
  publishing the new child. Solo start and the stop/start branch publish on
  status code alone — waiting with no warm generation to fall back on would
  just be an outage.
- The sealed-inference index rebuild no longer wipes and reinserts every
  sealed row on every restart. Snapshot restore does a gap fill (one id scan,
  zero writes when the index is already there); full journal replay rebuilds
  from the diff journal in batched transactions off the publish path.

---

## What's in this release

| Area | Change |
| --- | --- |
| **Ready probe split** | `/ready` status 200 = can serve; body `recovery_complete` = warm (backlog drained **and** `WaitRecoveryRepairs` returned). Counters `sessions_total`/`sessions_recovered`/`sessions_failed`/`sessions_version_skipped`/`sessions_pending` promoted from log-only to body + Prometheus |
| **Warm cutover** | Overlap swap waits for `recovery_complete: true` on the new child's `/ready` body before the route swap (`VERSIOND_RECOVERY_TIMEOUT`, default 30m). Solo start and stop/start publish on status 200. Bail-outs: absent field → cold cutover; old-child death → publish immediately; hostDraining/ctx → abort; timeout → old keeps serving |
| **Sealed-inference index** | Snapshot restore: gap fill only, no wipe (O(sealed) writes → 0). Full replay: rebuild from diff journal in batched transactions off the publish path. `WaitObsRepairs` → `WaitRecoveryRepairs` (waits both validation-obs and sealed-index repair) |
| **Nonce eviction** | A live session that fails `types.ErrInvalidNonce` is evicted and re-recovered (reuses `resolutionFailures` negative-cache so a bad client cannot spin the reload). Required for warm cutover: a long warm-up leaves the new child behind the old generation's writes |
| **versiond** | `getChildRecoveryStatus` reads the `/ready` body; `getHTTPStatus` stays status-only so `watchChildReadiness` never gates on the body (pinned by test) |

---

## Breaking / operator-facing changes

### `/ready` status vs body

The single probe that used to answer both "can serve" and "is recovery done"
is split:

| Signal | Meaning | Who reads it |
| --- | --- | --- |
| Status code 200 | Chain up, storage open, not draining | `waitForChildServingReady`, `watchChildReadiness`, K8s readinessProbe |
| Body `recovery_complete: true` | Recovery backlog drained **and** `WaitRecoveryRepairs` returned | `waitForChildRecoveryComplete` (overlap swap only) |

A pre-v5 `devshardd` does not ship `recovery_complete` on its `/ready` body.
The new `versiond` detects the absent field and skips the wait, cutting over
cold (flow B semantics) — so a mixed-version estate (new `versiond` + old
`devshardd`) keeps the v4 cold-cutover behaviour rather than hanging.

### Rolling updates (same name, new sha256) — warm before traffic, overlap only

The v4 rule was "new ready before traffic". The v5 rule is "new **warm** before
traffic, overlap only":

| Storage mode (both old + new `--print-storage-mode`) | Swap behavior |
| --- | --- |
| Exactly `postgres` | Blue/green: start new child on a new port → wait admin `/ready` 200 + public `/healthz` 2xx → **wait `recovery_complete: true`** → route new traffic to new generation → drain old → SIGTERM |
| `sqlite`, `hybrid`, `auto`/unknown, legacy binary without the flag | Exclusive stop/start (no overlapping children, no warm wait) |

Operator knobs (all defaulted; see [rolling-update.md](./rolling-update.md)):

| Env | Default | Role |
| --- | --- | --- |
| `VERSIOND_READY_TIMEOUT` | `60s` | Abort swap if incoming child never becomes *able to serve* (old keeps serving) |
| `VERSIOND_RECOVERY_TIMEOUT` | `30m` | Abort overlap swap if the new child's `recovery_complete` does not flip true in time (old keeps serving). **Not** the 60s ready timeout — recovery of a long journal is minutes to hours |
| `VERSIOND_DRAIN_TIMEOUT` | `15m` | Max wait for old proxy leases + lifecycle inflight |
| `VERSIOND_DRAIN_KILL_GRACE` | `10m` | Legacy no-status cushion / process kill backstop |
| `DEVSHARD_SHUTDOWN_GRACE` | `10m` | `devshardd` graceful HTTP shutdown after SIGTERM |

Bail-outs from the warm wait (companion *ready-on-boot-warm-cutover* flow D),
all of which cut over or abort rather than hang:

| Condition | Action |
| --- | --- |
| `recovery_complete` absent from the body | Skip the wait, cut over cold (flow B; an un-updated child cannot know about the field) |
| Old child stops being `Running` | Abandon the wait and publish immediately — a warming child plus a dead old child is an outage |
| `hostDraining` or ctx done | Abort, stop the new child |
| `VERSIOND_RECOVERY_TIMEOUT` elapsed | Abort, old keeps serving, retry next reconcile |

### Sealed-inference index rebuild

`RebuildSealedInferenceIndex` no longer runs unconditionally on every session
recovery. It is split by recovery path:

| Path | Behaviour |
| --- | --- |
| Snapshot restored (`replayFrom > 1`) | `FillSealedInferenceIndexGaps` inline: one `SealedInferenceIDs` scan, insert bare rows only for ids with no stored row. Never deletes, never downgrades an `ObsPresent = true` row. Normally writes nothing. |
| Full replay (`replayFrom == 1`) | `RebuildSealedInferenceIndexFromDiffs` in the background behind `ObsRepairGate`: wipe, then insert rich rows built from the diff journal in batched transactions. |

Net effect on the incident node (~1.5M sealed inferences): snapshot restart
goes from ~1.5M writes to **zero writes** (~0.3–0.6s of reads). Full replay
goes from ~25 min to ~11–17 s, off the publish path.

### Nonce eviction

Required on the same change as the warm wait: a child that recovered early
during a long warm-up sits behind the old generation's writes to the shared
store. The first request then fails `types.ErrInvalidNonce`. The new path
drops the stale session from `HostManager.sessions`, `Close`s the host, deletes
escrow metrics, and re-runs `recoverStoredSession` through the same
singleflight as `getOrCreate`. A bogus nonce is negative-cached via
`resolutionFailures` so a bad client cannot spin the reload; a genuine
catch-up mismatch uses a short TTL, not `permanentFailureTTL`.

---

## High-availability deployment

Topology, shared-Postgres diff/persist, validation leases, and
`versiond-router` host evacuation are unchanged from v4. See
[release-0.2.14-v4.md](./release-0.2.14-v4.md) §"High-availability deployment"
and [high-availability-architecture.md](./high-availability-architecture.md).

The v5 addition is the warm-cutover gate above; everything else (router
drain, failover, lease exclusivity) carries over.

---

## Upgrade from v4

v5 is a rolling update from v4 under the same version name (governance publishes
a new SHA). Because the warm wait bail-outs handle a mixed estate:

- **New `versiond` + new `devshardd`**: full v5 behaviour — overlap swap waits
  for `recovery_complete`.
- **New `versiond` + old `devshardd`**: the old child's `/ready` body has no
  `recovery_complete`; the new `versiond` skips the wait and cuts over cold
  (v4 behaviour). No outage, no hang.
- **Old `versiond` + new `devshardd`**: the old `versiond` does not read the
  body; it publishes on status 200 (v4 behaviour). The new `devshardd`'s
  `recovery_complete` field is simply ignored. No outage.

So v5 can roll out incrementally without coordinating a simultaneous
`versiond` + `devshardd` upgrade.

---

## Test coverage

The warm cutover is pinned at three layers:

- **Unit — `devshardd` body shape** (`devshard/cmd/devshardd/lifecycle_test.go`):
  `/ready` returns 200 while recovery is still draining and flips the body's
  `recovery_complete` to `true` only after `WaitRecoveryRepairs` returns;
  draining stays 503.
- **Unit — `versiond` wait + bail-outs**
  (`versioned/internal/process/manager_recovery_wait_test.go`): the overlap
  wait returns on `recovery_complete: true`; skips on an absent field (pre-v5
  child) and on a legacy child with no admin listener; publishes immediately on
  old-child death; aborts on `hostDraining`, context cancel, and
  `VERSIOND_RECOVERY_TIMEOUT` (old keeps serving). A pin test asserts
  `watchChildReadiness` never reads the body, so recovery never evicts the host
  from the HAProxy pool.
- **Integration — testenv boot tests**
  (`devshard/testenv/citest/versiond_warm_cutover_test.go`,
  `make citest-versiond-warm-cutover`): `TestVersiondWarmCutoverBoot` pins the
  status-vs-body split end to end — the public `/healthz` is 200 with
  `VERSIOND_RECOVERY_TIMEOUT` configured and a chat round-trip serves after
  boot. `TestVersiondWarmCutoverOverlapWaitsThenServes` pins the swap half — a
  SHA flip produces the `running(new)` + `draining(old)` overlap (which only
  appears after the warm wait returns), new traffic serves, and the old child
  retires. The admin `/ready` body is loopback inside the versiond container on
  a dynamic port, so the body field and bail-outs are unit-pinned; the testenv
  suite pins the end-to-end effect (the wait returns, the swap completes). See
  [`testenv/docs/scenarios.md`](../testenv/docs/scenarios.md) §"Versiond warm
  cutover".

