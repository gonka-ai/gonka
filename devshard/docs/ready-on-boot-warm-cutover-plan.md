# Ready on boot, warm on cutover

Companion to [sealed-inference-index-rebuild-plan.md](sealed-inference-index-rebuild-plan.md),
which removes the per-escrow write storm this design would otherwise push onto
the lazy-load request path.

## Problem

`devshardd` `/ready` currently answers 503 until session recovery finishes
(`cmd/devshardd/server.go:39` includes `recovered` in the 503 condition). Versiond
gates the `Starting → Running` transition on that probe with a 60s
`VERSIOND_READY_TIMEOUT` (`versioned/internal/process/manager.go:169-170`), so a
host with a long journal is force-stopped and restarted forever, never serving.

The fix is to separate two different questions that one probe is answering today:

- **Can this process serve?** Chain reachable, storage open, not draining. True
  within seconds of boot.
- **Is this process warm?** The recovery backlog is drained, so no request pays a
  cold replay. Minutes to hours.

Status code answers the first. A body field, `recovery_complete`, answers the
second. Only one caller needs the second: a version replacement that has a
healthy old generation to keep serving in the meantime.

## Flows

### A. v4 versiond, solo restart or boot

Spawn → `waitForChildServingReady` sees admin `/ready` 200 immediately and public
`/healthz` 2xx → `statusRunning`, `rebuildRoutes`, published. Traffic arrives;
escrows lazy-load through `getOrCreate` with the demand priority queue from
`7e704795f`. The 1s monitor keeps seeing 200.

**This is the fix.** Today the child is killed at the 60s `ReadyTimeout`.

### B. v4 versiond, version replacement (postgres overlap)

Unchanged and cold. `waitForChildReady` returns as soon as `/ready` is 200
(`manager.go:1020`), routes swap, old drains, every escrow lazy-loads after
cutover.

Accepted tradeoff: an un-updated versiond cannot know about the field, and per
the staleness finding below, cold is the safe direction.

### C. v5 versiond, solo restart or boot

Same as A. Publish on status code; read `recovery_complete` only to log and
export progress. No gate, because there is no warm generation to fall back on —
waiting would just be an outage.

### D. v5 versiond, version replacement (postgres overlap)

New behaviour. After `waitForChildReady` returns and **before** the
`m.processes` swap at `manager.go:1028-1055`, poll admin `/ready` and wait for
`recovery_complete: true`.

That window is already traffic-free: `runChild` calls `rebuildRoutes` only if
`m.processes[name] == c` (`manager.go:1690`), and during overlap that is still
the old child, so the new one is `Running` but unpublished. Then swap,
`rebuildRoutes`, `drainAfterProxy(old)`.

### Bail-outs

All of these cut over or abort rather than hang:

| Condition | Action |
| --- | --- |
| `recovery_complete` absent from the body | Skip the wait, cut over (old child, flow B semantics) |
| Old child stops being `Running` | Abandon the wait and publish immediately — a warming-but-unpublished child plus a dead old child is an outage |
| `hostDraining` or `ctx` done | Abort, stop the new child |
| `VERSIOND_RECOVERY_TIMEOUT` elapsed | Abort, old keeps serving, retry next reconcile |

## The staleness finding

Pre-warming is only safe with a self-heal. During a long warm-up the old child
keeps applying diffs to the shared store, so a session the new child recovered
early is behind by the time it is published. The first request then fails with
`types.ErrInvalidNonce` and there is no eviction path — the stale session stays
resident.

So the child-side eviction (workstream item 3 below) is **not optional**; without
it, flow D is less safe than flow B.

## Workstreams

### Child (devshardd, this branch)

1. `/ready`: drop `recovered` from the 503 condition; keep `recovery_complete` in
   `readyStatus`.
2. Promote the recovery counters from log-only to state and body:
   `sessions_total`, `sessions_recovered`, `sessions_failed`,
   `sessions_version_skipped`, `sessions_pending`. This is what makes "backlog
   drained with 3 failures" visible instead of indistinguishable from clean.
3. Evict and re-recover on `types.ErrInvalidNonce`, reusing the existing
   `resolutionFailures` negative-cache pattern so a genuinely bad client cannot
   spin the reload.
4. Prometheus gauge for recovery progress, so swap progress is observable
   without parsing probe bodies.

Tests: `/ready` 200 during recovery; 503 while draining and when storage is
unready; body counters; nonce-mismatch eviction reloads and then succeeds;
eviction is rate-limited.

### Versiond (v5)

1. Probe helper returning status plus `recoveryComplete *bool`. `getHTTPStatus`
   (`manager.go:2059`) is status-only today.
2. `VERSIOND_RECOVERY_TIMEOUT`, default 30m. Must not reuse the 60s
   `ReadyTimeout`.
3. The wait, in the overlap branch of `downloadAndSwap` only.
4. The bail-outs from flow D.
5. Gate applies only to devshard children with an admin port
   (`devshardAdminEligible` plus a non-zero admin port).
6. `watchChildReadiness` untouched, pinned by a test that it never consults the
   body — if recovery ever gates the monitor, the host leaves the HAProxy pool
   for the whole backlog.

Tests: overlap waits then cuts over; absent field skips; old-child death
abandons the wait; timeout aborts with the old child still routed; solo start and
the stop/start branch never wait.

### Docs

`rolling-update.md` §1.3 probe table and the swap section; v4 release notes. The
rule changes from "new ready before traffic" to "new warm before traffic,
overlap only".

## Sequencing

1. Child items 1 and 3 together — item 1 alone makes flow A work but exposes
   flow D to staleness, and item 3 is the mitigation.
2. Sealed-inference gap fill (companion doc, steps 1–3) — otherwise every
   lazy-loaded escrow pays ~1.5M writes on a request goroutine.
3. Child items 2 and 4, the observability half.
4. Versiond items, which are useless until the child ships the field.

## Out of scope

- Returning `503 Retry-After` instead of blocking on singleflight for a first
  request to an unrecovered escrow. A client-contract decision, tracked
  separately.
- Old versiond against a new child: accepted cold cutover (flow B).
- Repairing validation obs for an escrow booted from a snapshot whose obs rows
  are empty. Pre-existing.
