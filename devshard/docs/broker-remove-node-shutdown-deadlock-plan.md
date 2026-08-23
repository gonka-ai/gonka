# Broker `RemoveNode` shutdown stall / deadlock — fix plan

**Component:** `decentralized-api/broker`
**Branch:** `fix/broker-remove-node-shutdown-deadlock`
**Related:** [PR #1494](https://github.com/gonka-ai/gonka/pull/1494) (`fix(broker): prevent panic submitting to shut-down worker`) — same code path, complementary, should land first
**Status:** plan — not started
**Base:** verified against `upgrade-v0.2.16` @ `7b7e4098a`. Line references below are for this tree; on `main` the only relevant difference is that [#1536](https://github.com/gonka-ai/gonka/pull/1536) split the send into a private `submit(ctx, cmd, generation)` behind `Submit`.

Deleting a node can park the broker's **only** command-processing goroutine for up to 15 minutes, and — once a buffer fills during that window — park it forever. Nothing about this requires an unusual configuration: one unresponsive ML node and one `DELETE /admin/v1/nodes/:id` is the whole reproduction.

---

## The cycle

Four independently verifiable facts compose into a wait cycle:

| # | Fact | Location |
|---|---|---|
| 1 | `RemoveNode.Execute` runs on the broker's single command goroutine (`go broker.processCommands()`), which dispatches every command serially | `broker.go:396`, `broker.go:421` (`case RemoveNode`) |
| 2 | It calls `RemoveWorker` → `Shutdown` → `wg.Wait()`, blocking that goroutine until the worker's in-flight `Execute` returns | `node_admin_commands.go:280`, `node_worker.go:175`, `node_worker.go:107` |
| 3 | Nothing cancels the in-flight context. `reconcile` stores `cancelInFlightTask` but `RemoveNode.Execute` never calls it, and reconcile contexts are `context.WithCancel` — **no deadline** | `broker.go:1033`, `broker.go:1040` |
| 4 | The worker reaches `wg.Done()` only *after* `w.broker.QueueMessage(updateCmd)`, a **blocking** send on `highPriorityCommands` (capacity 100) | `node_worker.go:71` and `:83`, `broker.go:462`, `broker.go:325` |

Facts 1–3 give a bounded stall; fact 4 turns it unbounded.

### F1 — head-of-line block, up to 15 minutes

`Shutdown` waits for an in-flight `Execute` that nobody cancelled. The mlnodeclient HTTP timeout is **15 minutes** (`mlnodeclient/client.go:35`), so that is the bound per in-flight command. All four worker commands do thread `ctx` into every client call (`node_worker_commands.go`), so they *would* unwind promptly if cancelled — the mechanism exists and is already used by reconcile's phase 1 (`broker.go:993`–`1002`); the removal path simply doesn't use it.

For the duration, **no broker command executes at all.** Blast radius, worst first:

- `LockAvailableNode` / `ReleaseNode` are on the inference request path (`node_lock.go:31` `AcquireMLNode`, `lock_helpers.go:109`). Node acquisition stops, so the participant stops serving inference — even though every other ML node is healthy.
- The triggering HTTP request hangs too: the handler blocks on `node := <-response` (`internal/server/admin/node_handlers.go:36`).
- Once `lowPriorityCommands` (10000) and `highPriorityCommands` (100) fill, `QueueMessage` itself blocks and the stall propagates into every HTTP handler that queues a command.

### F2 — permanent deadlock, escalating from F1

The worker can only reach `wg.Done()` after its blocking `QueueMessage`. If `highPriorityCommands` is full, `run()` never gets there, `wg.Wait()` never returns, and **the only goroutine that would drain that buffer is the one parked in `Wait`.** Restart is the only exit.

F1 is what makes F2 likely rather than exotic: during a multi-minute stall the producers keep queueing high-priority commands — one `UpdateNodeResultCommand` per reconciling node, `SetNodesActualStatusCommand`, `SyncNodesCommand` every 60 s, `RegisterNode` batches from `POST /admin/v1/nodes/batch` — so a fleet around 100 nodes fills 100 slots. Note the drain loop inside `run()` (`node_worker.go:83`) has the same blocking send, so shutdown-time draining is equally exposed.

### Relationship to PR #1494

Same path, neither subsumes the other. #1494 stops `Submit` from **panicking** while a worker shuts down; this work stops `Shutdown` from **stalling the broker**. They conflict textually (both edit `Shutdown`), and #1494 is smaller and already reviewed, so it lands first and this branch rebases onto it. There is also a functional reason for that order: step 1 below can leave a worker goroutine alive after `Shutdown` returns, and a `Submit` racing that window is exactly the panic #1494 removes.

---

## Decisions

| # | Question | Decision |
|---|---|---|
| D1 | How to stop waiting on a hung ML call | **Cancel before waiting.** `RemoveNode.Execute` reads `State.cancelInFlightTask` under `b.mu` and calls it before `RemoveWorker`. Reuses the existing mechanism and its reconcile precedent — no new plumbing. |
| D2 | Trust cancellation alone? | **No — bound the wait too.** A future command could ignore `ctx`, or be stuck in a non-cancellable section. `Shutdown` gets a deadline; expiry logs loudly and proceeds. This is also what demotes F2 from permanent to bounded. |
| D3 | Timeout value | **5 s.** Long enough that a cancelled HTTP call unwinds normally (so the graceful path stays the common path), short enough that no operator reads it as a hang. |
| D4 | Move the wait off the broker goroutine entirely? | **Not in step 1.** It is the structurally cleanest answer but changes result-ordering semantics on removal. Filed as step 2, to be judged on its own once step 1 has removed the pathology. |

**Why a bounded wait is safe.** After the deadline the worker goroutine may still be running and may still deliver its `UpdateNodeResultCommand` for a node that has since been deleted. That is already handled: `UpdateNodeResultCommand.Execute` logs `Received result for unknown node` and returns (`commands.go:172`–`181`). The abandoned goroutine is bounded by the 15-minute client timeout and exits on its own, since `shutdown` is already closed.

## Non-goals

- Re-architecting the broker away from one serialized command goroutine.
- Making the worker's result publication non-blocking. Dropping a result leaves `ReconcileInfo` set forever, and reconcile skips any node with `ReconcileInfo != nil` (`broker.go:1007`), so the node would never be reconciled again — strictly worse than a bounded wait. See step 3.
- Fixing the pre-existing flaky `TestNodeWorker_QueueFull` (15/40 failures under `-race` on untouched code; it asserts exactly 10 accepted submits while `run()` concurrently drains). Separate PR.

---

## Implementation steps

### Step 1 — Cancel, then wait with a deadline *(required; the whole fix)*

**Files:** `broker/node_worker.go`, `broker/node_admin_commands.go`, `broker/node_worker_test.go`, `broker/broker_test.go`.

1. `NodeWorker.ShutdownWithTimeout(d time.Duration) bool` — closes `shutdown` (idempotently, per #1494), then waits on `wg` with a deadline, returning whether it drained cleanly. Implement the bounded wait by having a goroutine `wg.Wait()` and `close(done)`, then `select` on `done` vs `time.After(d)`. Keep `Shutdown()` as `ShutdownWithTimeout(defaultWorkerShutdownTimeout)` so existing callers and tests are unchanged.
2. On expiry, log at Error with `node_id` and elapsed, naming the likely cause (in-flight ML call or a full broker queue). This is the operator's only signal that a worker was abandoned.
3. `NodeWorkGroup.RemoveWorker` deletes from the map **regardless** of the return value — a worker that failed to drain must not stay reachable.
4. `RemoveNode.Execute` cancels first:

```go
b.mu.Lock()
cancel := (func())(nil)
if node, ok := b.nodes[command.NodeId]; ok {
    cancel = node.State.cancelInFlightTask
}
b.mu.Unlock()
if cancel != nil {
    cancel()
}
b.nodeWorkGroup.RemoveWorker(command.NodeId)
```

`b.mu` must be released before `RemoveWorker`, because the in-flight `Execute` may itself take `b.mu` (e.g. `resolveSupportedNodeModelID`) — holding it across the wait would substitute one deadlock for another. Note that the existing comment "Remove the worker first (it will wait for pending jobs)" needs updating: the wait is now bounded.

**Exit:** with an ML node that never answers, `DELETE /admin/v1/nodes/:id` returns in well under a second (cancellation path) and the broker keeps processing `LockAvailableNode` throughout. With `highPriorityCommands` artificially full, removal returns within the 5 s deadline instead of never.

### Step 2 — Take the wait off the broker goroutine *(optional follow-up)*

`RemoveNode.Execute` unregisters the worker and hands `ShutdownWithTimeout` to a reaper goroutine, so the command loop never blocks on a worker at all — even for the 5 s worst case, and even if D3's value is later found too generous. Deferred deliberately: it makes a removed node's result land after deletion by design rather than by accident, which deserves its own review rather than riding along with a stall fix.

### Step 3 — `ReconcileInfo` watchdog *(separate issue, separate PR)*

Independent latent bug found while tracing this one: if a result is ever lost, `ReconcileInfo` stays non-nil forever and reconcile skips that node permanently (`broker.go:1007`). Phase 1 only cancels when the *intended* status changed (`broker.go:983`–`989`), so a node whose target never changes is stuck for the process lifetime. Wants a timestamp on `ReconcileInfo` and expiry after a few reconcile intervals. Listed here only so the connection is recorded — it must not expand this branch.

---

## Test plan

All of these are unit tests in `decentralized-api/broker`, run by the existing **Build and Test API Wrapper** job (`go test ./...`). No CI wiring needed. Every test must be deterministic — no sleep-until-hopefully-done.

### F1 — the broker loop keeps running

`TestRemoveNode_DoesNotBlockBrokerLoop`. Register a node with a real broker, submit a command whose `Execute` blocks on `<-ctx.Done()` and records that it saw cancellation, set `State.cancelInFlightTask` as reconcile would, then queue `RemoveNode`. Assert, with channel waits rather than sleeps:

- the blocking `Execute` observed cancellation (proves D1 fired, not just that the timeout expired),
- a `LockAvailableNode` queued **after** `RemoveNode` gets its response promptly,
- `RemoveNode`'s own response arrives promptly.

Fail-first: without the cancel in step 1, this test blocks until the 5 s deadline, so give the assertions a budget well under it (a few hundred ms) and it fails on unfixed code for the right reason.

### F2 — a full broker queue cannot wedge shutdown

`TestNodeWorker_ShutdownWithFullBrokerQueue`. `NewTestBroker2(1)`, pre-fill the single `highPriorityCommands` slot, submit one command, wait until `Execute` has started (signal channel), let it finish so `run()` blocks in `QueueMessage`, then assert `ShutdownWithTimeout(100ms)` returns `false` within ~2× the deadline instead of hanging. Fail-first on today's code: it hangs and the test times out.

Then drain the broker channel and assert the abandoned worker's result is delivered and handled as `unknown node` — pinning the D2 safety argument rather than trusting it.

### Regression / no-behaviour-change

- `TestNodeWorker_GracefulShutdown` must still see all 5 queued commands execute — the deadline must not truncate the normal path.
- New: clean drain returns `true`; timed-out drain returns `false`; `RemoveWorker` deletes the worker from the group in **both** cases (`GetWorker` reports gone).
- New: `RemoveNode` for an unknown node id, and for a node with `cancelInFlightTask == nil`, both no-op without panicking.
- Whole package under `-race -count=1`, and the new tests under `-race -count=10`. Expect only the pre-existing `TestNodeWorker_QueueFull` flake (documented above under Non-goals) — anything else is ours.

### Manual / staging check

Point a node config at a host that accepts TCP but never responds (or `DROP` the port), let reconcile dispatch to it, then `DELETE /admin/v1/nodes/:id`. Before: the request hangs and inference requests stop being assigned nodes. After: the request returns immediately and inference continues.

---

## Risks

| Risk | Mitigation |
|---|---|
| Abandoned worker goroutine after a timed-out drain | Bounded by the 15-minute client timeout; exits on its own via the already-closed `shutdown`; late result handled as `unknown node` (`commands.go:172`) and asserted by a test |
| A `Submit` racing the abandoned worker | Precisely what [#1494](https://github.com/gonka-ai/gonka/pull/1494) fixes — land it first; without it that race is a process panic |
| 5 s still too long for the inference path | Step 2 removes broker-loop blocking entirely; D3 is a constant, trivially tunable |
| Cancelling in-flight work leaves the ML node mid-transition | Already the semantics of reconcile phase 1 (`broker.go:993`); the node is being deleted, so no local state depends on the outcome |
| Textual conflict with #1494 in `Shutdown` | Rebase onto it; the idempotency guard from #1494 is a prerequisite for closing `shutdown` safely here |
