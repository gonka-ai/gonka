# Proposal: Escrow rotation temp create vs active retire (M12)

**Status:** Future work / deferred  
**Related:** PR #1366 review finding **M12** — temps can fail while actives already retired  
**Scope:** Make bridge-epoch escrow rotation fail-safe so routing capacity does not shrink when temp create is incomplete.

This is a design note only. **Not scheduled for the current PR / immediate fix set.**

---

## Problem

`Gateway.prepareBridgeEscrows` (`escrow_rotator.go`):

1. Calls `ensureRotationEscrows(..., rotationRoleTemp, ...)` to create/bridge **temp** escrows for the model.
2. On **success**, retires previous **regular/active** escrows via `retireRotatedDevshard`.

If temp create fails, the path falls back to `promoteActiveRegularEscrowsToTemp`, which only relabels store metadata (`RotationRole` / `RotationEpoch`) — it does **not** create replacement on-chain capacity.

Partial or failed temp creation combined with successful retirement of actives (or a later retry that retires after a partial ensure) can shrink mid-epoch routing capacity for the model until the next successful finish/create cycle.

---

## Proposed fix (describe — future)

Options to evaluate (pick one or combine):

1. **Gate retire on full temp readiness** — only retire regulars when `CreatedCount + ExistingCount >= TempCount` (or an explicit “bridge capacity OK” check), not merely when `ensureRotationEscrows` returns nil.
2. **Two-phase commit** — mark temps ready, then retire; on incomplete temps, leave regulars active and retry next tick.
3. **Promote with capacity** — if create fails, keep regulars active for routing (do not retire); treat promote-to-temp as bookkeeping only for epoch tagging, never as a substitute for missing temps when retiring.
4. Tests covering: partial temp create, create error + promote, and “no retire until temp count met.”

---

## Out of scope (for now)

- Implementing the rotation reorder / gate in this proposal / current merge.
- H5–H7, M8, M11 comment-only alignment, Lows.

---

## Acceptance sketch (when implemented)

- A failed or incomplete temp create never leaves the model with fewer usable active escrows than before the tick solely because regulars were retired early.
- Promote-to-temp remains metadata-only and does not unlock retirement of the last regulars without replacement capacity.
