# Proposal: forced settlement of a busy escrow

**Status:** Implemented
**Scope:** `devshard/cmd/gateway` — `api`, `escrow`, `operations`, package docs
**Related:** [escrows.md](../../cmd/gateway/docs/escrows.md), "Settlement and retirement" · [rules.md](../../cmd/gateway/docs/rules.md) §1

Every claim about current behaviour below was read in the code on `custom/gateway/v5` and is cited inline.

---

## Problem

`POST /v1/admin/devshards/{id}/settle` answers 409 `devshard busy` whenever the escrow still has a request in flight. The guard sits twice on the path: once in the handler ([api/admin_devshards.go:145](../../cmd/gateway/api/admin_devshards.go)) and once inside the manager ([escrow/settlement.go:127](../../cmd/gateway/escrow/settlement.go)), where `IsBusy` is `entry.inFlight.Load() > 0` ([registry/escrow.go:41](../../cmd/gateway/registry/escrow.go)). An operator who wants an escrow closed now has no way to say so.

Two facts make the current answer worse than it looks.

**The refusal reports a half-finished operation.** `Manager.settle` parks the escrow *before* it checks busy: `park` writes inactive and pending in one statement and then stops routing, and the busy check runs after it. The code says as much — "busy is a deferred-settle signal, not a failure: the now-retired escrow drains, then a retrigger settles it." So the 409 is returned for an escrow that has already left routing and is already marked `SettlementPending`. The caller sees a failure and the system has half-committed.

**The retrigger does not always exist.** `settlePending` returns immediately when `Rotation.SettlementEnabled` is false ([escrow/settlement.go:29-31](../../cmd/gateway/escrow/settlement.go)). On a gateway with automatic settlement switched off, a manual settle that lands on a busy escrow parks it and nothing retriggers it.

That second fact is a consequence of the toggle rather than a defect of it, which an earlier draft of this proposal got wrong. `retire` reads the same switch and, when it is off, **parks only** — "Parked only: the row names the sole key that can settle this escrow later" ([escrow/settlement.go:211-216](../../cmd/gateway/escrow/settlement.go)). Parked-and-unsettled is the documented outcome the switch exists to produce, so draining pending rows regardless of it would contradict the setting rather than repair it. The operator's remedy for a stranded row is to call settle again, which is exactly what the change below makes succeed.

---

## Why forcing is safe enough to offer

Forcing does not break the invariant that a committed nonce is always settled ([rules.md](../../cmd/gateway/docs/rules.md) §1), because the state machine already has a deterministic outcome for work that is still live when settlement runs.

`Manager.settle` calls `Finalize` before it builds the settlement. Finalize moves the phase off `PhaseActive`, and `applyStartInference` refuses outright at any other phase with `ErrSessionFinalizing` ([state/machine.go:1161](../../state/machine.go)). No new nonce can be committed after that point, forced or not.

Records still live at the Finalizing-to-Settlement drain take the documented default: `settleLiveRecordLocked` turns `StatusStarted` and `StatusPending` into `StatusFinished` with `ActualCost = ReservedCost`, credited to the record's executor slot ([state/machine.go:1679-1692](../../state/machine.go)). Nothing is orphaned.

So the cost of forcing is precise and worth stating in the operator docs rather than leaving to be discovered: **the in-flight requests fail for their callers, and their full reserved cost is paid to the hosts that held them, whether or not any answer was delivered.** That is a trade an operator may legitimately want — an escrow bleeding into a broken fleet, an epoch boundary, a fleet being drained — and it is theirs to make, not the gateway's.

---

## Change 1 — `force` on the settle endpoint

`POST /v1/admin/devshards/{id}/settle?force=true`.

- Thread a `force bool` through `Operations.Settle` to `Manager.Settle` and `Manager.settle`. It skips exactly one branch: the `IsBusy` check at [settlement.go:127](../../cmd/gateway/escrow/settlement.go). The handler's own pre-check at [admin_devshards.go:145](../../cmd/gateway/api/admin_devshards.go) skips with it.
- Everything else on the path is unchanged: `settlements.enter` still refuses a settlement another caller is already running, `alreadySettled` still short-circuits, `Finalize`, `BuildSettlement` and the broadcast are untouched, and the row is still deleted only on a confirmed broadcast.
- The audit line names the force and the count it overrode, so the decision is legible afterwards: `auditAdmin("escrow settled under force", "escrow", id, "in_flight", n)`.

**What `force` must not reach.** `ErrSettlementInFlight` is a deduplication guard against two concurrent settlements of the same escrow, not a busy signal — forcing past it would broadcast twice. `handleAdminDevshardDelete` keeps its guard; deleting a busy escrow removes its session storage underneath live requests, which is a different and unrecoverable operation. The standalone `POST /v1/admin/devshards/{id}/finalize` guard is left as it is for now; `settle` finalizes on its own path, so forcing there is the operation actually being asked for.

## Change 2 — withdrawn

An earlier draft proposed draining `SettlementPending` rows regardless of `Rotation.SettlementEnabled`. Reading `retire` settled it: that switch means "park, do not settle", and pending-and-unsettled rows are its intended product. Change 1 closes the operator's hole on its own, so nothing here is needed.

What remains worth doing, and is not done here: the unforced 409 does not say that the escrow was already parked and is draining. The status is right — the operation did not complete — but the message reads as "refused, nothing happened" when the truth is "parked, finishing on its own". That is a wording change in `api/errors.go` and belongs to whoever next touches that surface.

---

## Alternative considered

**Drain with a deadline instead of forcing.** After `park`, poll `IsBusy` until it clears or a caller-supplied budget expires, then settle; force only if the budget runs out. Routing has already stopped by then, so in-flight work is bounded by the engine's own deadlines and this would usually settle without failing a single user request. It is strictly kinder than `force` and strictly slower, and it does not remove the need for `force` when a host is hung rather than slow. Worth layering on later as `?drain_ms=`; not in this change, which is about giving the operator the override they asked for.

---

## Verification

`go build ./... && go vet ./... && golangci-lint run && go test -race ./...` for the tree.

Behaviour, as tests written red first:

- A settle with in-flight requests and no force answers 409 and leaves the escrow parked and pending.
- The same settle with `force=true` proceeds: finalize, build, broadcast, row deleted.
- Force does not cross `ErrSettlementInFlight`: a second concurrent forced settle of the same escrow is still refused.
- Force does not re-broadcast an escrow `alreadySettled` reports as settled.
- The delete endpoint still refuses a busy escrow, with or without the flag.
