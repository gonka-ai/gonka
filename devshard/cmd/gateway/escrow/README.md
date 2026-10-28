# `escrow` — the escrow's life

An escrow is funds on chain plus a group of hosts. This package creates one, keeps it honest across restarts and epoch boundaries, and closes it.

## What it owns

| File | What it holds |
| --- | --- |
| `manager.go` | the lifecycle: create, activate, drain, retire |
| `rotation.go`, `commitments.go` | replacing an escrow across the proof-of-compute boundary, and the intent recorded before the transaction so a crash cannot lose it |
| `epochs.go` | the effective epoch a snapshot reads as, and the label a bridge temp carries |
| `settlement.go` | closing an escrow and paying what its hosts earned |
| `deadline.go` | parking an escrow whose chain epoch's settlement window is closing, whatever the rotation toggle says |
| `settle_margin.go` | measuring the block time and narrating a settle margin shorter than two settle windows |
| `staleness.go` | reading deadlines past a stale chain snapshot at a projected height |
| `depletion.go` | marking an escrow routing would retire, for the tick to drain |
| `checker.go`, `dedup.go` | crash-recovery reconciliation: what the chain holds versus what this process recorded |
| `chain_facts.go` | reading each escrow's chain epoch and amount once its create lands, and lazily, sixteen rows a tick |
| `budget.go` | which rows count against `max_unsettled`, and how many a model holds (`unsettledCount`) |
| `breaker.go` | refusing to keep creating escrows when creation keeps failing |
| `planner.go`, `planner_state.go`, `apply_plan.go`, `planned_lifecycle.go` | the funding planner: reading each model's money, asking `funding.Plan`, narrating, reporting and executing the decision, and the tick's lifecycle around it |
| `vocabulary.go` | the strings written into rows and log fields: rotation roles and stages, which are stored, why a commitment was cleared, and the funding report's count states |

## Boundaries

- **Intent is written before the transaction, not after.** An interrupted rotation is recoverable only if the record of what was attempted survives the interruption.
- **An escrow parks before it is checked for busy, not after.** Otherwise a nonce can be committed between the two.
- **Settlement waits for the votes its nonces owe.** Concluding while one is in flight pays for work the chain has not yet judged.

## The tick

`Start` runs one tick immediately and then every `TickInterval` (15 s). `Stop` cancels the context the tick runs under, so a tick already in flight is interrupted, and blocks until it has exited — it is a barrier for every caller. Both are idempotent: a second call is a no-op rather than a second loop.

`TickInterval` is exported because `api` answers a drained offered model's 503 with it as `Retry-After`: the tick is the soonest the gateway itself plans, settles and republishes.

`runLifecycle` runs these steps in order (`manager.go`, `planned_lifecycle.go`):

1. `reconcile`: crash recovery must not depend on a runtime toggle.
2. `resolveChainFacts`: a money deadline reads the chain epoch whether or not rotation runs.
3. `checkSettleMargin`: the margin the deadline rule uses is only worth what the measured block time says; it narrates and never refuses, and runs first so a stale snapshot is projected with this tick's block time.
4. `parkAtDeadline`: an escrow whose chain epoch's settlement window is closing loses its whole amount if nothing parks it (see "Settlement by deadline").
5. `settleOffTick`: a parked escrow's row is the only record of which key can settle it, so nothing else will ever pick it up.
6. `markPrunedPastDeadline`: a row out of service that settlement will never pick up would hold its place in the unsettled count for good once the chain prunes it (see "Settlement by deadline").
7. `checkMissing`: an escrow the chain no longer holds must stop taking traffic.
8. `sweepTimeouts`: a nonce the chain will still settle is owed a vote whether or not rotation is on.
9. `promoteTakenReserves`: a reserve a request already took is a regular escrow in routing, so its row says so before the planner reads the model.
10. `markSpent`: a serving escrow routing would retire is marked even when no request reaches routing to report it (see "Depletion marks").
11. `drainPlannedMarks`: the marks are drained; a nonce cap retires its escrow.
12. `prepareBridge` within `PrePoCBlocks` of the epoch switch, otherwise `retireSurplusReserves` while requests are not blocked; both only when rotation is enabled, a model parses and the snapshot carries chain data (`EpochIndex` and `BlockHeight` both non-zero — otherwise it is a cold start).
13. `planFunding`: the funding planner; see "The funding planner", below.

Steps 1 to 8 run whatever the `Rotation.Enabled` toggle says, because each of them is about an escrow that already exists rather than about creating one; so do 9 to 11. `rotationModels` returns an empty set when rotation is off, so no caller downstream has to re-read the toggle.

`settleOffTick` and `sweepTimeouts` are the two steps that do not run *on* the tick. A finalize collects signatures from every host and waits out the slow ones, and a vote round can outlast 15 s as well; on the tick either would hold the retirements and funding of every other escrow, so a nonce-capped escrow would keep its place while a stranger finalizes. Each runs in its own goroutine, and `Stop` waits for both as well as for the tick. A second tick sweeps nothing while the first sweep is still at it; it does start the settlement of a parked escrow that is not already settling, so one finalize waiting out a slow host never holds another escrow's settlement. The settlement order is chosen on the tick (`parkedToSettle`), and no goroutine starts when nothing is parked.

The chain snapshot is pulled from the observer once per tick rather than subscribed to: at this cadence a poll is equivalent and it avoids callback races. The devshard rows are likewise loaded once, have their chain epoch and amount resolved where still missing (`resolveChainFacts`, see "Chain epochs and amounts"), and are passed down; the steps filter that one slice rather than reloading it, except `markSpent`, `drainPlannedMarks` and the bridge step, which read the slice `promoteTakenReserves` returns.

Besides the 15 s ticker, a taken reserve wakes the loop at once (`OnReserveTaken` → `wakeup`), and so does a money-short model (`OnMoneyShort`).

The lifecycle's errors and the planner's join in the tick error. A tick the ticker started is a **scheduled** tick and a wakeup is not; only the planner tells them apart.

## The funding planner

The planner always runs at the end of every tick and always executes its decision (`apply_plan.go`, `applyPlan`). It asks the regular create breaker through `gated`, so the cooldown counts down once per planning pass. Planned retires go through `retire` (a busy or in-flight settle is "not yet", not an error). Planned creates go through `createFor` with the planner's reason (a standby as a reserve); the first failed create stops the rest of that pass. A landed create resets the regular breaker key the planner gates on, even for a standby; a create refused before broadcast (wallet short, amount below floor) opens no breaker; any other failure opens the regular breaker and reaches the tick error. A wallet refusal marks the model `walletShort` (`recordCreate`), which the next decision reads as `funding.ModelState.WalletShort`; a landed create clears it, and so does any decision that wants no guard, capacity or spread create, so a stale refusal never presses the budget once the wallet is topped up. Once the decision has no spread shortfall, outside the bridge window and with requests not blocked, the active temps of the model are retired (`retireBridgedTemps`, narrated as `BridgeFinished` with zero created).

With rotation off, or a model list that does not parse, it reads nothing and drops every report, so no stale model is exported. Otherwise each tick reads the devshard rows and the commitment rows once more (`ListDevshards`, `LoadCommitments`); a failed read is narrated as `FundingPlanFailed` and leaves the money-short marks for the next tick, since they are drained only after both reads succeed. Then, for every model rotation names, builds one `funding.ModelState` ([`funding/README.md`](../funding/README.md), "Inputs") (`planner.go`, `readModel`, `readRow`):

| Input | Where it comes from |
| --- | --- |
| `Counted` | every row of the model except one gone from chain (`budget.go`, `goneFromChain`: inactive with `gone_from_chain` set), plus its commitment rows |
| `Parking` | the model's rows that are inactive with `settlement_pending` set: parked, or settling with a broadcast settle not yet confirmed. A settled row still waiting to be dropped is not parking, nor is a row gone from chain |
| per-escrow money | `Deps.Funds` (`FundingReader`): the registry's live session balance and prices, and `Registry.OpenRecords`, classified by `liquidity.Classify` against the escrow's own price, `user.TimeoutBuffer` and the sweep's grace. An active row the registry holds no active session for is counted `unread`. When its stored `amount` is known it enters the plan as the fresh escrow it is (`unreadEscrow`, `EscrowState.Unread`): its stored amount as free money, nothing in flight, its role and its label, classified at the model's prices at the chain's current price. It then counts in every judgement — the guard's `fullCount`, the liquid money capacity reads, spread, standby and the budget — but no planned retire chooses it, and it is not narrated starved or full, until a session is read for it. Without that, an escrow a commitment just recovered, or one created a tick ago whose publish has not landed, is invisible to the plan, and the next tick funds a second one. An unread row whose `amount` is unresolved stays out of the plan's escrows and counts only against the budget |
| prices | `Limits.RetirementReserve` for the full-context request, `scheduler.RequestCost`, and `scheduler.AttemptsToFund` for the slot |
| nonce cap | `ExhaustionProbe` naming `nonce_cap` |
| `MoneyShort` | the model was marked by `OnMoneyShort` since the last tick |
| gates | requests blocked, an epoch known, the network serving the model, the regular create breaker's cooldown, the bridge windows from `PrePoCBlocks` |
| `ScheduledTick` | true on the tick the 15 s ticker (or `Start`) started, false on a wakeup |

**The money-short hook** (`OnMoneyShort`, wired from the scheduler's `Deps.OnMoneyShort` through `depletionNotice` in `app/planner_wiring.go`) only marks the model in a `markSet`; it does no I/O. A money-short signal wakes the tick once per mark: the mark is drained by the next plan, so signals at request rate wake one tick, and the bucket bounds the creates.

**State between ticks** lives in `fundingPlanner` (`planner_state.go`): per model a `funding.Bucket`, a `funding.DemandWindow`, the surplus streak and the last episode flags; per escrow whether it was last seen full and since when it has been idle. The demand window records the model's reserved money only on a scheduled tick, and the surplus streak advances (or resets) only on a scheduled tick, so a burst of wakeups never skews the peak or counts as surplus samples. The bucket refills on wall time on every tick and spends one token per planned create. The state is created once in `NewManager`, so the demand window counts from boot; a model rotation no longer names, and an escrow no longer live, is forgotten at the end of the tick.

**What it says.** Through the narrator (`KindFundingTransition`, see [`journal/README.md`](../journal/README.md)): `EscrowStarved` when an escrow is first seen starved or has fallen from full to starved for two planning reads, `EscrowFull` when one has been full again for two planning reads (a one-read flap is not narrated), `FundingMisconfigured`, `FundingGuaranteeBroken` (an episode lasts until the guard shortfall is zero, not until the breaker stops gating) and `EscrowBudgetReached` once per episode, `EscrowPlanned` on every tick that decided a create or a retire, and `FundingPlanFailed` when a store read failed. Through `FundingReports`, which the composition root hands to `metrics.FundingCollector` (`fundingModels`, `app/planner_wiring.go`): per model the money by `liquidity` class, the escrows by `CountState` (`full`, `starved`, `spent`, `standby`, `unread`, `parked`, `inactive`, `committed`, every state present, at zero when no escrow is in it), the guarantee (`fullCount − full_context_slots`) and every decision counted by `funding` action and reason since boot.

## Creating an escrow

Every create — the planner's, the bridge's temps and an operator's `CreateEscrow` — goes down one path (`createFor`), so all three are recovered by `reconcile` in the same way. See [`docs/escrows.md`](../docs/escrows.md), "Creating an escrow".

1. Resolve the signer named by the model's `private_key_env`.
2. `onPrepared` writes the commitment row — model, role, epoch, key env, block height, tx hash, creation time. A failure there aborts before any chain broadcast: no broadcast without durable intent.
3. Broadcast, wait for the escrow id, register the devshard row, then drop the commitment.

Before step 2 the chain client reads the signer's spendable `ngonka` and refuses the create when it is below `amount` plus the fee (the fee counts only when it is paid in the same denom), so an empty wallet never reaches `onPrepared` or the broadcast (`chain/txclient.go`, `WalletUnderfundedError`). That refusal is a state of the wallet, not a failing chain, so it does not open the create breaker: an operator who tops the wallet up gets the next tick's create, not one up to four ticks later. It is narrated once per (model, role) as `EscrowCreateUnderfunded` and again only after a create of that pair has succeeded (`commitments.go`, `narrateUnderfunded`).

Earlier still, `createFor` refuses an `amount` below one full-context request of the model, priced from the snapshot it is handed: `config.Limits.RetirementReserve` priced by `scheduler.RequestCost` at the chain's `TokenPrice` and `FeePerNonce`, plus the `CreateDevshardFee` the chain deducts before the escrow's first balance (`commitments.go`, `creationFloor`). The floor is a minimum for creating an escrow, not a threshold that retires one: routing declines an escrow only for a request it cannot pay, `drainPlannedMarks` drops every mark but a nonce cap ("Depletion marks"), and a starved escrow keeps serving until the funding planner retires it by its own rules ("The funding planner"). The refusal is `ErrAmountBelowFloor`, which the admin API answers with 400; like a wallet refusal it opens no breaker (`rotation.go`, `refusedBeforeBroadcast`) and is narrated once per (model, role) as `EscrowCreateBelowFloor`. Until the observer has read a price, the check is skipped.

Every create names its reason in the `escrow created` line: the planner's `guard`, `capacity`, `spread` or `standby`, `bridge` for a temp `prepareBridge` funds, and `operator` for the admin create. A create recovered from its commitment row carries no reason; the commitment does not store one.

`persistEscrow` resets the create breaker on both of its paths. An escrow found already registered is a create that succeeded, and leaving the breaker tripped would back off the next create for a failure that did not happen.

### Reconciling a commitment row

`reconcileOne` asks the chain what became of the commitment's transaction. The four answers mean different things:

| Lookup result | What it means | What happens |
| --- | --- | --- |
| found | the escrow exists | register it, drop the commitment |
| committed, no escrow event | terminal: no escrow will ever exist | drop the commitment |
| `ErrTxNotFound`, inside the grace window | the unordered tx may still land | keep the row, retry next tick |
| `ErrTxNotFound`, past the grace window | it can no longer land | drop the commitment |
| transport error | nothing is known — the endpoint is unreachable | keep the row, retry next tick |

A create whose broadcast the node refused at `CheckTx` (`chain.ErrTxRejected`) drops its commitment at once, narrated as `transaction rejected at broadcast`, rather than holding it for the grace window: that transaction can never land, and the row would otherwise count against the model's unsettled room until the window ends. Any other broadcast error keeps the row for `reconcileOne`.

The grace window is `commitmentReconcileGrace`: the chain's `UnorderedTxTTL` plus `commitmentIndexLagMargin` (2 min), which allows for a landed transaction staying unqueryable a little past its TTL. A row with a zero `CreatedAt` — malformed, or written before the stamp existed — counts as still pending, because keeping a commitment costs one row while dropping a live one costs an escrow nobody knows about.

`clearCommitment` takes the reason it narrates rather than deriving it: the two callers know it, the row does not.

## Chain epochs and amounts

A row's `rotation_epoch` says which bridge set the escrow belongs to: a regular or reserve is labelled with the latest epoch when it was created, a bridge temp with the epoch it bridges into (see "The bridge across proof-of-compute"). The chain moves the latest epoch to N+1 at PoC start while it stamps the escrow with the effective epoch, N, so the label is not the chain's epoch. The row therefore also keeps `chain_epoch`, the epoch the chain stamped (`EscrowInfo.EpochIndex`), and `amount`, what the chain locked (`EscrowInfo.Balance`). Both are read with one `GetEscrow` right after a create registers its row (`persistEscrow`), and lazily for every other row: each tick, before `settleOffTick`, `resolveChainFacts` reads up to sixteen rows still missing either (`chainFactsPerTick`), skipping rows gone from chain, and hands the tick the rows with what it read. The walk goes over the unresolved rows in escrow-id order and starts after the last row the previous tick read (`chainFactsAfter`), wrapping round, so a row that never resolves — pruned, unstamped, or behind a lookup that keeps failing — takes its turn and never starves the rows after it. A failed read, an escrow the chain does not hold, an answer without an epoch, or a failed write leaves the row as it was, and it is tried again on its next turn. The first failure of a row is narrated as `EscrowChainFactsUnresolved` with its reason (`lookup_failed`, `not_on_chain`, `no_epoch`, `write_failed`; `vocabulary.go`), and not again until the row resolves or leaves the unresolved set. Resolution runs only when the manager has `Deps.ChainFacts`, which the composition root always wires to the chain client; a manager without it, as in most unit tests, reads and writes nothing.

## The bridge across proof-of-compute

`prepareBridge` swaps temp escrows in ahead of an epoch switch, one model at a time, isolating failures: a model that fails is recorded in the rotation status and skipped, never stopping the rest. Once PoC is over the funding planner's spread create replaces the temps with regulars, and the temps are retired once the spread is met (`apply_plan.go`, `retireBridgedTemps`, which picks them with `isActiveTemp`).

`fillToLabel` creates temps up to the target count for each (model, label). It stops before creating anything for a model in two cases:

- **The network serves no such model** — `servedByNetwork` reports known-and-not-served. That is narrated, because it is a rotation that produced nothing by design: without the line an operator looking for the escrow that never appeared would find no reason anywhere. In `prepareBridge` it leaves the model short of its temps, so the regulars are promoted, not retired (see "A partial fill"). A cold start where both weight-by-model maps are empty reads as *unknown* rather than *not served*, so nothing is skipped.
- **The create breaker is gated**, which returns `errCreateSuppressed`. That is not "nothing needed": the breaker is gated exactly when creation has been failing, so `prepareBridge` must take its degrade path and keep the escrows it has instead of retiring them for replacements that were never created.

**A temp is labelled with the epoch it bridges into**, the effective epoch plus one (`bridgeLabel`, `epochs.go`; `effectiveEpoch` reads `EffectiveEpochIndex`, or the latest epoch while that is unknown, 0). `prepareBridge` counts the temps it already has against that label (`fillToLabel`, `createsWanted`) and writes it on the commitment and the row (`createFor`). The label does not move inside one bridge window: before PoC the latest and effective epochs are both N, inside PoC the latest is N+1 and the effective still N, so a window that crosses PoC start, or one a long PoC holds entirely, counts the same temps under N+1 and funds one set. Counting by the latest epoch, as before, funded a second set at PoC start. The planner's regulars and standbys keep the latest epoch, and `retireBridgedTemps` retires temps labelled at or below the latest epoch, which after the switch is the label they carry. From the `set_new_validators` block on, the snapshot's switch height names the next switch (`chain/README.md`), so the bridge window has closed by the time the effective epoch moves and no set labelled one further is funded.

Two numbers describe one bridge. `EscrowCreated` and the temp's row carry the label, N+1; `BridgePrepared` and the `rotation_status` row carry the snapshot's latest epoch, which is N before PoC starts and N+1 inside it. A bridge whose window crosses PoC start therefore narrates `bridge prepared ... epoch N` with temps created at `epoch N+1`.

An unknown effective epoch (the reply lacks the latest epoch's PoC start or its `set_new_validators`, `EffectiveEpochIndex` 0) reads as the latest. Inside PoC that labels temps one too high, N+2: they are not counted against the set already labelled N+1, so a second set can be funded, and `retireBridgedTemps` leaves them until the latest epoch reaches their label. It needs the chain reply to drop a stage height inside PoC, which it does not normally do. The deadline consequence: a temp labelled N+2 whose `chain_epoch` is also still unresolved reads its settlement deadline from the label less one, N+1, one epoch late, until lazy resolution fills `chain_epoch` (within minutes).

The degrade path relabels the role only: a regular promoted to temp keeps its label, N, and does not satisfy the N+1 temp count. While the window lasts, each tick finds no temp at the bridge label and tries the create again; the failure that started the degrade usually stops it there — an underfunded wallet or a below-floor amount is refused before broadcast, and a broadcast failure feeds the create breaker, whose cooldown suppresses the next attempts — and the promoted escrows keep serving. Promoting does not relabel because the store's role write (`SetDevshardRotationRole`) deliberately touches the role alone.

`prepareBridge` creates through `fillToLabel`, which funds one create per model per pass and loops until every model reaches its target or stops on an error. A wallet that can pay for only some creates then spreads them across models instead of filling the first-listed models and leaving the rest with nothing; an underfunded create stops only its own model. The creates all run before any retirement, and each model's retirement still waits on its own fill having succeeded.

The degrade path is `promoteRegularsToTemp`: it relabels the existing regulars in place so the epoch still has bridge coverage, and keeps going past a write failure.

**The bridge is capped at the model's room.** A model with `max_unsettled` set funds at most `max_unsettled − unsettledCount` new temps (`bridgeRoom`): `unsettledCount` is every row of the model the chain still holds — serving, parked or operator-deactivated, everything but a row gone from chain — plus every open commitment, which is an escrow that may already exist. The temps already at the bridge label count toward `temp_count` on top of that room. A model with `max_unsettled` 0 or below is absent from the room map and uncapped, and when no model is capped the commitments are not even loaded.

**A partial fill.** A fill that leaves the model with fewer temps at the bridge label than `temp_count` — no room, a network that does not serve the model, or creates stopped part way — is `errBridgePartialFill`: the model takes the degrade path, so its regulars are promoted and keep serving, and none is retired with nothing in its place. The rotation status records the error (`create_error`), rewritten on every tick while the room stays short, but the tick does not fail on it: it is an outcome of the budget, not a broken create. A temp that was created before the fill stopped serves beside the promoted regulars, and `retireBridgedTemps` retires both once the planner's spread is met.

`deferredRetire` separates "not yet" from "failed". `ErrDevshardBusy` and `ErrSettlementInFlight` are both retried by the next tick, so surfacing them would make an error of every ordinary rotation; anything else is a real failure and reaches the tick.

### The create breaker

The breaker is keyed by (model, role). A failed create opens a cooldown of `escalatedCooldownTicks` — doubling per consecutive failure, capped at `maxCreateBreakerCooldownTicks` (4). The cooldown is counted in ticks, meaning calls to `gated`, not in wall-clock time, and a successful create resets the key.

## Depletion marks

An exhausted escrow is exactly the one the load score prefers, because its in-flight count stays low while it fails every request, so `OnBalanceExhausted` only marks it and the next tick drains the marks (`planned_lifecycle.go`, `drainPlannedMarks`). A nonce-cap mark retires the escrow through `retire`; a balance mark is dropped and counted in `devshard_gateway_planner_ignored_marks_total`, so the escrow keeps serving what it can pay. `markSpent` (`depletion.go`) marks a serving escrow no request reached, asking `ExhaustionProbe` — the scheduler's `Exhaustion`, through `escrowExhaustion` in `app/routing.go` — about each active row. See [`docs/escrows.md`](../docs/escrows.md), "Depletion".

## The reserve

A request is served by one escrow, so what a large prompt needs is one escrow that can cover it, not a pool whose sum can. Eight escrows drained evenly each hold an eighth of what one fresh escrow holds, and a request priced above that — a 400k-token prompt reserves its body's bytes — fits none of them, while none of them is retired as long as each still covers the retirement floor — the model's context length as prompt bytes at four bytes per token — which a prompt denser in bytes than that estimate can exceed. The reserve is the escrow kept full for that case.

The funding planner funds up to `reserve_count` (default 1, 0 turns it off; `models.go`) escrows per model in the `reserve` role as standbys (`standby`), at the model's own `amount`, never inside the pre-PoC window or while requests are blocked: `prepareBridge` retires every non-temp escrow, a reserve included (see [`funding/README.md`](../funding/README.md)).

Routing picks a reserve only when no regular escrow was picked and every regular failed for money, never for its hosts (busy, an unusable weight, or none the allowlist admits), and taking it turns it into a regular escrow in the registry at once; `OnReserveTaken` marks it and wakes the tick, and `promoteTakenReserves` rewrites the row's role to `regular` before the planner reads the model, after which it counts toward `target_count` like any regular and the planner funds the next standby. A failed role write is marked again for the next tick. `retireSurplusReserves` retires a reserve no longer wanted — one an earlier epoch funded that the bridge did not retire because the gateway missed the pre-PoC window, or one past a lowered `reserve_count`, 0 included. The bridge's degrade path, which relabels regulars as temps when temps cannot be funded, leaves a reserve in its role (`promoteRegularsToTemp`). After a crash between the take and the tick, the row still reads `reserve` and the escrow is published as one again; it is used only when regulars cannot pay, and promoted again then. See [`docs/routing.md`](../docs/routing.md), "A reserve escrow".

## Settlement and retirement

See [`docs/escrows.md`](../docs/escrows.md), "Settlement and retirement", for the states this walks through.

`retire` reads the `SettlementEnabled` toggle; `PreviousEpochSettlementEnabled` never settles at retirement, only in `settleOffTick` and the deadline pass once the escrow's chain epoch is over (`settlementPolicy.settles`). With settlement off the escrow is only **parked**: its row carries the private-key env name that is the sole way to settle it later, so the row outlives retirement and is dropped only once a settlement has actually been confirmed on chain. With settlement on the escrow settles first, and the row is dropped only once the settle succeeded — a busy, deduped or failed settle leaves it registered, inactive and pending.

`park` writes inactive and settlement-pending in one statement, so a crash leaves the escrow recoverable, and only then stops routing to it — which is what the busy check that follows relies on.

`settle` runs in a fixed order:

1. **Dedup** on the escrow id. A concurrent caller gets `ErrSettlementInFlight` rather than success, because the row carries the only key that can settle the escrow and belongs to that other caller.
2. **Park** — before the reconciliation, not after. The caller deletes the row on success, and a row that is gone can no longer take the escrow out of routing, so an escrow put back by hand would keep serving with nothing left to un-publish it.
3. **Reconcile** against the transaction this escrow last broadcast (below).
4. **Busy check**. `ErrDevshardBusy` is a deferred-settle signal, not a failure: the now-retired escrow drains, and a retrigger settles it.
5. Resolve the signer, sweep, finalize, build the settlement input. The sweep (`sweepBeforeFinalize`) votes the execution timeouts left due with no race grace (`TimeoutSweeper.SweepBeforeFinalize`), because finalize pays every open record its full reserve and an escrow parked across a restart has no session the tick sweep reaches. One attempt posts at most `TimeoutSweep.BudgetPerTick` votes, and at most `pendingSettleBudget` plus `urgentSettleBudget` settles run at once, so a tick's votes stay within one budget for the tick sweep plus one per settle in flight, still independent of the request rate. When the whole budget was due more may be, so an unforced settle that landed at least one vote returns `ErrDevshardBusy` and the next tick sweeps on; each such deferral settled at least one record, so the backlog drains. An unforced settle whose sweep failed a vote returns `ErrDevshardBusy` as well, for `failedSweepRetryWindow` (four ticks, one minute) from the first such failure: finalize pays the failed record its full reserve, a verifier blip or a host restart clears within that, and a vote the verifiers keep refusing cannot hold the settlement back past it. The window is timed on the manager's clock rather than counted in attempts, so a retirement settle, the off-tick pass and an operator's retry in one tick do not use it up between them. It is kept in memory and closed once a settle goes on to finalize or the row is dropped, so a restart opens it again. A forced settle never waits: past the margin the settlement itself is at stake, and an escrow still deferring when it enters its margin settles forced, so neither deferral can carry it past the chain deadline. A zero budget turns this sweep off with the tick's. A settle whose context ends during the sweep returns the context's error instead of finalizing, and a finalize a cancel cuts short resumes on the next settle (`user.Session.Finalize`).
6. Record the tx hash before the broadcast, broadcast, and only then clear `settlement_pending`. Any earlier failure leaves it set for recovery.

`settleOffTick` drains parked escrows in deadline order (`settleDeadline.order`: a passed deadline first, then the earliest `settleBy`, rows without a deadline last, ties by escrow id). A row inside its margin is settled with force, past the busy check, and does not count against the budget; the rest settle at most `pendingSettleBudget` (4) at once, those still settling from an earlier tick included, and margin rows at most `urgentSettleBudget` (8) at once. `admitParked` picks the rows and takes their slots before any settle starts, so the bounds hold within a pass and across passes; each admitted row settles in its own goroutine, a row any settle already has in flight is left to it, a busy or in-flight escrow stays parked without an error, and a failing one stays parked and reaches the tick error under its escrow id. `Settle` is the operator's entry point onto the same path, so it carries the same dedup, deactivate-first ordering and busy check — and it drops the row itself, because retirement drops it on its own paths and the row's only purpose was naming the key this settle just used.

### Reconciling a settle that may already have landed

`alreadySettled` reads the hash and broadcast stamp the row carries and asks the chain about it:

| Answer | What happens |
| --- | --- |
| committed and succeeded | reported as settled; no second transaction is built |
| committed and rejected | the hash is cleared, so a retry is right |
| not found, inside the grace window | `ErrSettlementInFlight` — an unordered transaction stays landable for its whole TTL, and rebroadcasting inside that window pays a fee per tick for a settle still on its way, then loses to it and starts over |
| not found, past the grace window | the hash is cleared: a fresh transaction is right |
| transport error | fails the tick — an unreachable endpoint is not an answer, and must not be read as "the settle never happened" |

A settle whose broadcast the node refused at `CheckTx` (`chain.ErrTxRejected`) clears the hash it recorded in the same call, so the next tick builds a fresh transaction instead of answering `ErrSettlementInFlight` until the window ends. A transport error or any other broadcast error keeps the hash, and the table above decides on the next tick.

A settle row with no broadcast stamp defaults the opposite way to a commitment row: it counts as **past** the window. Keeping a commitment costs a row, while keeping a hash here costs an escrow that is never settled at all. Rebroadcasting one that was in fact still landing costs a fee once, and the stamp that write leaves governs every tick after it.

## Settlement by deadline

`deadline.go`. The chain settles an escrow only while the effective epoch is its chain epoch E or E+1, and prunes it unsettled at E+2, splitting its whole amount across the group. The bridge retires escrows only when rotation is on and the models list parses, and a temp whose finish could fund no regular survives it, so `parkAtDeadline` runs on every tick between `resolveChainFacts` and `settleOffTick`, whatever `Rotation.Enabled` says, and needs no model to be configured.

**The deadline** (`deadlineAt`). The chain epoch is the row's `chain_epoch`, else `rotation_epoch − 1`, the lower bound (`chainEpochOf`); a row with neither has no deadline. The effective epoch is the snapshot's, else the latest, the upper bound (`effectiveEpoch`), so an unknown either way reads the deadline early, never late. At effective E the deadline is at least one epoch away. At effective E+1 the escrow must settle before the switch into E+2, the snapshot's `EpochSwitchBlockHeight` (`settleBy`); at exactly the switch block into E+1 the snapshot already names the next switch, so a row of epoch E does not count the switch it has just crossed. A row is **inside its margin** once `BlockHeight ≥ settleBy − SettleMarginBlocks` (`rotation_settle_margin_blocks`, 600 by default), or at once when the snapshot carries no switch height. At E+2 or later the deadline has **passed**. Nothing is decided on a cold start (no epoch in the snapshot) or without `Deps.ChainFacts`.

**What a row inside its margin gets.**

| Row | Action |
| --- | --- |
| serving, starved or a standby reserve, its model's settlement or previous-epoch settlement on | parked (`park`), narrated `EscrowDeadlineReached`; `settleOffTick` settles it on the same tick, first in line, forced past the busy check and outside the budget |
| any row whose model has settlement off, and previous-epoch settlement off or its own epoch not provably over | narrated `EscrowDeadlineUnsettled` `settlement_disabled`, never parked or settled against the toggle |
| inactive, not parked, no settle hash: operator-deactivated | narrated `EscrowDeadlineUnsettled` `operator_deactivated`, or `key_missing` when the environment cannot resolve its key, never settled automatically: the operator chose to stop it |
| already parked | left to `settleOffTick` |
| gone from chain | skipped: nothing is left to settle |

A row whose deadline has passed is narrated `deadline_passed` as well, without a `settle_by` field because the switch block is no longer known, and an active one is still parked, so `settleOffTick` finds it pruned and drops it (`ErrEscrowPruned`). An absent answer counts as pruned only past that deadline, read on the effective epoch's floor, for the row's `chain_epoch` or, while that is unresolved, for its label, the latest epoch the chain can have funded it in (`prunableOnChain`, `latestChainEpochOf`); earlier it is a lagging node, and the settle goes on and is retried. See docs/escrows.md, "An escrow the chain has pruned". Each `EscrowDeadlineUnsettled` reason is narrated once per row while the row stays in that state (`deadlineNarrated`), not every tick.

**What settlement will never pick up is marked gone** (`markPrunedPastDeadline`, each tick after `settleOffTick`). A row out of service whose deadline has passed — at no margin, on the unprojected snapshot — and that settlement does not own (parked, with its model's settlement policy settling it) is looked up on the chain, sixteen a tick in escrow-id order after where the previous tick stopped (`prunedCheckAfter`, the same walk as the chain facts' `nextInTurn`), so rows the chain still holds cannot starve the rest. Once the chain answers it does not hold the escrow — it prunes an epoch's escrows at E+2 and splits what is left (`DevshardPruningThreshold`) — the row is retired from routing, marked `gone_from_chain` and narrated `escrow gone from chain`, as a host-reported missing escrow is ("Escrow checks"). The row stays as the record of the loss but leaves the unsettled count, the chain-facts walk and this rule, so it is not narrated `deadline_passed` again after a restart. A row whose settlement was confirmed but whose drop failed (a settle hash, no longer pending) is dropped instead (`deleteSettled`), because it is no loss. A lookup error or a failed write leaves the row as it was and returns into the tick's errors; only a confirmed not-found marks. It runs after `parkAtDeadline`, so the loss is narrated first, and leaves a parked row settlement owns to `settleOffTick`, which drops it when it finds the escrow pruned. A serving row is never marked here, and neither is one inside its deadline, because a lagging node answers not found for an escrow created a moment ago.

A settle that never commits is rebroadcast once its transaction's TTL has passed (see "Reconciling a settle that may already have landed"), so a margin longer than two settle windows (`chain.UnorderedTxTTL` plus the index-lag margin, each) leaves room for one retry before the deadline.

**The margin is checked against the measured block time** (`settle_margin.go`, `checkSettleMargin`, each tick before `parkAtDeadline`, with `Deps.ChainFacts` set). `blockPace` remembers the first height it saw and the snapshot's update time (`LastUpdatedAt`, else now); once the height is at least 100 blocks past it (`blockPaceMinimumBlocks`), the block time is the time since then over the blocks since then. A height below the first starts the measurement over. A margin whose blocks at that pace are shorter than two settle windows is narrated `SettleMarginShort` — once per episode: not again while the same margin stays short, and again after it has been long enough or `settle_margin_blocks` changed. Nothing is refused: the margin is the operator's, and the line says how short it is.

A chain snapshot older than `chain_snapshot_max_age_seconds` is read at a projected height: its height plus the time since it was taken over the measured block time, or one block a second before a block time is measured, so a frozen snapshot reads a deadline early rather than missing it; the first such tick narrates `escrow deadlines read past a stale chain height`. The block time is measured from each snapshot's own update time, so a frozen snapshot does not stretch it. An inactive row whose key the environment cannot resolve is narrated `key_missing`; boot marks a row the chain lacks gone from chain instead of deactivating it.

## Escrow checks

`TriggerEscrowCheck` deactivates an escrow only on a **confirmed** not-found. A row already out of service reaches the same mark through the deadline instead (`markPrunedPastDeadline`, "Settlement by deadline"). A lookup error and a found escrow both keep it active: ambiguity is never a reason to deactivate. A found escrow the chain reports `Settled` is parked instead, which takes it out of routing and leaves the row to the settle pass to drop; when no row is left to park, as after this gateway's own settle dropped it, the check is done rather than failed, so it does not schedule itself again. Concurrent callers for the same id dedup to one check. When the chain does confirm the escrow is gone, routing stops before the row is written, so an escrow the chain confirms absent takes no further request even if that write fails. A confirmed not-found marks the row gone from chain (`MarkDevshardGoneFromChain`: inactive, off hold and `gone_from_chain` in one statement). `MarkDevshardGoneFromChain` leaves `settlement_pending` and the settle hash as they were, so every reader gates on the mark: a gone row no longer counts against `max_unsettled`, is never parking in the planner (it is reported `inactive`), is never picked by `settleOffTick`, and is not resolved for its chain facts, since a gone escrow has nothing left to settle. Reactivating a row clears the mark in the same write (`store/README.md`, "The devshard registry"), so a reactivated row that is later parked counts and settles like any other; `goneFromChain` also requires `active = 0`, so the mark never hides a serving row.

## Keeping the request path off the chain

Two hooks are called from the request path — `OnEscrowMissing` and `OnBalanceExhausted` — and neither does I/O. Each only marks an escrow id in a `markSet`, and the next tick takes the whole set at once, which is what keeps a per-request event from fanning out into a per-escrow chain call.

`markSet.drain` steals the map rather than copying it, so a key marked while the tick is running belongs to the following tick and cannot be dropped. `mark` reports whether the key was new, which is how a depletion is logged once per tick rather than once per request. A step that fails re-marks its key, so a failed check never un-schedules itself.

`inFlightSet` is the other half: it dedups concurrent operations by key. The first caller enters and gets a `leave` func to call when done; a caller whose key is already in flight is told it is busy and should no-op.

## What this package expects of others

- `escrowTxClient`, satisfied by `*chain.TxClient`. `TxCommitted` is what tells a row still marked pending apart from one whose settle genuinely failed: the settle may have reached the chain after the wait gave up.
- `escrowStore`, satisfied by `*store.Store`; `snapshotSource`, satisfied by `*chain.PhaseObserver`.
- `escrowLookup` (`Deps.ChainFacts`), satisfied by `*chain.TxClient`: the `GetEscrow` that resolves a row's chain epoch and amount. Without it nothing is resolved (see "Chain epochs and amounts").
- `FundingReader` (`Deps.Funds`), satisfied by `escrowFunds` in the composition root (`app/planner_wiring.go`) over the registry. Without it the planner reads nothing and executes nothing.
- `SettlementSource`, satisfied by `*registry.Registry` and wired by the composition root (`app/compose.go`). `Retire` is synchronous — no nonce can be committed on the escrow after it returns — and that is what makes `IsBusy` monotone, so an idle answer stays true until the settlement it gates is broadcast. `Finalize` is idempotent.
- **A narrator** (`Deps.Narrator`, satisfied by the journal) hears every transition an operator reads the log for — created, recovered, cleared, gone, marked, rotation skipped, regulars promoted to temp, bridged, parked, settled, reconciled, dropped, a create refused as underfunded or below the model's floor, a reserve taken, a failed tick, a sweep that found work, and the funding planner's lines (see "The funding planner"). The package writes no line itself, and every escrow id it hands over is the text form the rest of the gateway uses.
- `ModelConfig`'s json tags are the `DEVSHARD_ESCROW_ROTATION_MODELS_JSON` wire contract and are not renameable.
- `max_unsettled` (default target + temp + reserve + 4, floor target + temp + reserve + 1) and `full_context_slots` (default 2, at least 1) are per-model fields of the same JSON, read by the escrow planner.

## Read next

- [`docs/escrows.md`](../docs/escrows.md) — the states, the transitions, and what each one costs.
