# Maintenance Windows Proposal - Feedback Response Summary

Thanks for the review. We updated the proposal and task plan based on your feedback.

## Changes We Made

### D1: Reservation lifecycle state machine

Accepted.

We updated the proposal to make reservation lifecycle transitions explicit and block-driven:

1. `Scheduled -> Active` in `BeginBlock` when `block_height == start_height`
2. `Active -> Completed` in `BeginBlock` when `block_height == start_height + duration_blocks`

We also updated the task plan to add explicit lifecycle implementation work and called out that the lookup path must be fast enough for begin-block execution.

We also tightened the proposal to require exact-height `BeginBlock` transitions and bounded lookup behavior, while moving the concrete storage/index format into the task plan.

### D2: Epoch-critical phase conflicts

Accepted.

We added explicit scheduling rejection rules for windows that overlap:

1. The PoC commit / exchange phase
2. The DKG phase

We also updated the task plan to add explicit implementation work for these scheduling rejections and their corresponding query behavior.

### D3: Credit storage layout

Accepted.

We changed the proposal from storing maintenance credit on the participant record to using a dedicated per-participant `MaintenanceState`, keyed by participant address and separate from the hot participant object.

`MaintenanceState` now carries both:

1. Maintenance credit
2. The last epoch in which maintenance was activated
3. The active reservation reference, if any
4. The next scheduled reservation reference, if any

This keeps maintenance accounting decoupled from the participant record without splitting it into multiple fragmented per-participant maintenance buckets.

We also added a new rule that a participant may have at most one future scheduled maintenance window at a time.

### D4: Activation-time concurrency re-check

Accepted with a clarification.

We updated the proposal to re-check concurrency caps at activation time in `BeginBlock`, but we are not hard-canceling windows at activation.

If a reservation activates under current params that would now exceed caps:

1. The reservation still activates
2. A warning event is emitted
3. Advisory warning / violation metadata is stored on the reservation so it is queryable later

This preserves operator predictability while making governance drift visible.

### D5: Credit accrual during maintenance-used epochs

Accepted.

We updated the proposal so that:

1. Ordinary reward eligibility remains unchanged
2. Maintenance-credit accrual is suppressed in any epoch where a maintenance window was activated for that participant

This makes every maintenance use have a net credit cost and resolves the original self-replenishing-credit concern.

## Additional Notes

We also added the following clarifications while incorporating the feedback:

1. The `BeginBlock` lifecycle and lookup path must use direct keyed Cosmos SDK collections access rather than broad iteration.
2. Activation-time warnings are not event-only; they are also made queryable on the reservation itself.
3. The task plan now includes explicit work in both the maintained Cosmos SDK fork and the inference-chain repo.
4. The task plan now includes end-to-end coverage for the restricted PoC / DKG scheduling rules.
5. The proposal now states required performance properties at a high level, while the task plan carries the concrete storage and index layout.
6. The task plan now explicitly separates:
   - exact-height transition schedule for `BeginBlock`
   - start-height overlap index for scheduling-time range scans

## Updated Documents

The following documents were updated:

1. `proposals/maintenance-windows/maintenance-windows.md`
2. `proposals/maintenance-windows/maintenance-windows-todo.md`
