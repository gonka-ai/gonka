# `scheduler` — which escrow, which host, which nonce

A chat request needs a nonce, and a nonce is bound to a host by `nonce % groupSize`. This package decides which escrow to spend from, and what to do when the nonce it is handed belongs to a host that cannot serve.

## What it owns

- **Escrow choice** (`request_pick.go`, `escrow_pick.go`) — a load score over the live escrows, scaled by the chain's own host weights and carrying the burns each escrow's nonce cursor is about to cost.
- **One actor per escrow** (`dispatcher.go`, `dispatcher_queue.go`) — the only goroutine that advances that escrow's nonce stream, so two requests can never take the same nonce.
- **The match decision** (`match.go`, `decision.go`) — for the nonce just offered, either serve a waiting request, hold it briefly for a compatible one, burn it, or decline.
- **Burns** (`ghost.go`) — a nonce that will serve nobody, named by the reason it was burned, because an operator reads the reason to decide what to do.
- **Divergence blocks** (`divergence.go`) — a host whose post-state-root disagrees with the escrow gets one catch-up replay; after that it is blocked for that escrow.

## What it does not own

It does not dispatch. It hands out an assignment and the [`engine`](../engine/) sends the request. It does not decide what a burn means for a host either: it names the reason and reports it, and the ledger excludes ghosts from every host rate by construction, because burning a nonce is the gateway's own decision (`accounting/findings.go`). The fan-out from one burn to its log line, its metric and its ledger entry lives in the composition root, not in a package of its own (`app/observers.go`, `tracedDispatches`). A nonce served on a host its request excluded reaches the same observer as `ExcludedHostServed`, and a nonce sent over a full congestion window as `ForcedSend`, so the scheduler writes no line of its own.

## Boundaries

- **Seven host gates, in this order**: outside the allowlist, proof-of-compute-required, congestion windows full (or the host's advertised chat budget spent, see routing.md, "A host with no chat budget"), cut off, ejected, state-diverged, then excluded by this waiter. A participant named by `scheduler_unthrottled_participants` is not asked the first, third and fifth of those; the other four hold for everybody. See routing.md, "Unthrottled participants". The first six need no waiter and are memoised once per drain; only the seventh depends on who is asking, which is why `match`, `servable` and the escrow pick's burn forecast read the same `blocks` definition rather than three ladders. The forecast reads it unmemoised and across escrows, because the memo is keyed by participant alone and one escrow's state block is not another's. A capability refusal is counted, never routed on.
- **A full window is the one gate a burn run may cross.** After `scheduler_max_consecutive_burns` burns in a row the next binding is sent over it rather than burned; a cut-off, which holds a host already found broken, is never crossed. See [routing.md](../docs/routing.md), "The forced send".
- **Predicates are frozen for the whole drain.** Reading them live lets a host look usable to the sweep that kept a waiter and unusable to the binding that would serve it — which burns a nonce every turn, forever.
- **The divergence block and the spent replay outlive the escrow's actor.** Reaping an idle dispatcher is idleness, not resolution.
- **A burn decided before the session could commit has no nonce to name**, and is reported without one.

## Picking an escrow

A request that names no escrow is scored across the candidates for its model; a request that pins one by id skips the score and is checked against the nonce ceiling, because that is what reserves room for the finalize and settlement that follow. A pin is never reported for money: it is declined with `ErrPinnedEscrowShort` only when its escrow cannot pay this attempt.

- **The score** is the ascending utilisation ratio `(ActiveUsers + queued waiters + expectedBurns) / EscrowWeight`. A non-positive or NaN weight scores unusable rather than best.
- **`expectedBurns` is the distance from the escrow's nonce cursor to a slot that can take this request**, walked over the same `blocks` ladder the drain reads, past the waiters already queued on that escrow. It is added to the in-flight count rather than multiplied into the ratio, because an escrow with nothing in flight would otherwise score zero however far its cursor sits from a usable host. A group no slot of which can take the request is priced at one lap and still picked when it is the only one: the pick predicts, the drain decides, and the drain's sweep refuses without spending a nonce. See [routing.md](../docs/routing.md), "Pricing an escrow by the burns it will cost".
- **A drain that answers `ErrHostsBusy` earns one more escrow.** `Pick` routes a second time, once, excluding the escrow that gave up, and only for an unpinned request; when the second round finds nowhere to go the first escrow's answer stands, because a busy shard must not read as a missing one. See [routing.md](../docs/routing.md), "One re-pick when an escrow gives up".
- **A full submit queue walks to the next escrow.** An unpinned request whose escrow's dispatcher answers `ErrEscrowBusy` is offered to the next escrow in the ranking, leaving out every escrow whose queue already refused it, under the same pick rules and bounded by the candidate count; `ErrEscrowBusy` reaches the caller only once every candidate is full. See [routing.md](../docs/routing.md), "Past a full queue".
- **The allowlist is read here as well as at dispatch.** An escrow whose whole group the allowlist refuses can never serve, and picking it by load alone spends the request on a group holding nobody. When every candidate is unreachable that way the caller gets `ErrAllowlistUnreachable`, because waiting cannot fix it. An empty allowlist skips the walk entirely: the narrowing exists only where an operator asked for it.
- **Ties hold indices, not candidates.** An index does not escape the way a returned `Escrow` does, so the common case picks without touching the heap. The tie-break is a single counter shared across every model and tie-set shape.
- **The nonce cap** uses `fallbackNonceCeiling` — the fixed ceiling applied while governance `max_nonce` is unread — until governance `max_nonce` has been fetched. A fetched cap is lowered by `nonceInFlightMargin`, or by half the cap where that is smaller. The fetched value is clamped to `MaxUint32`, never allowed to wrap: a cap wrapping to 0 makes `MaxActiveNonce` return `^uint64(0)` and disables the gate.
- **The balance floor asks two questions and prices them differently.** "Can this escrow pay for this request" is priced the way the chain charges it, `(input_length_bytes + max_tokens) × token_price + fee_per_nonce`, and only skips the candidate. "Is this escrow full" prices the largest request the model's hosts accept — its context length, never less than one capped answer — paid once per attempt, and reads nothing from the arriving request. Neither question adds the requests already on the escrow: the session's balance is net of their reservations already. It ranks a starved escrow that affords every attempt of this request ahead of a full one (the residue tier) and retires nothing: the money gate is the request's own price, with the funded and thin tiers. See [capacity.md](../docs/capacity.md), "The balance floor". Every multiplication is overflow-checked, because a price this escrow cannot afford must not read as an affordable small one.
- **Reserves rank last, and a take is reported once it served.** Regular escrows are ranked first; only when that tier picks nothing, and passed no regular over for busy hosts, an unusable weight or an allowlist that admits none of its group, is the same walk run over the model's reserves. A reserve is reported through `Deps.OnReserveTaken` only once the dispatcher has handed the request a nonce on it (`request_pick.go`, `pickOnce`), so a pick that fails leaves it a reserve. See [routing.md](../docs/routing.md), "A reserve escrow".
- **Money-short is reported once per unpinned pick.** When an unpinned pick for a model that has candidates ends declined for the balance floor, `Deps.OnMoneyShort` is called with the model (`escrow_pick.go`, `pickEscrow`); a pin, an allowlist refusal, a pick declined only by the nonce ceilings and a pick that succeeds never call it. `AttemptsToFund` exports the attempts an escrow is priced for. See [routing.md](../docs/routing.md), "Picking an escrow".
- **Exhaustion is reported, not just acted on.** Routing only declines an exhausted escrow; replacing it belongs to the rotation lifecycle, which otherwise never learns and lets the escrow drain silently into `ErrNoEscrowCapacity`. The fallback ceiling is never reported, so only the hosts' nonce cap is: `exhaustionReason` reports the nonce cap alone, and a balance never retires an escrow.

See routing.md, "Picking an escrow".

## The drain

One pass of `drain` assigns nonces until the queue empties, a nonce is held, or the burn budget trips. The freeze and the budget are what bound its cost in nonces; the budget is `groupSize × (waiting + 1)`. It returns with a waiter still queued **only** when it reports a held nonce — the one exit the loop arms a timer for — so nothing is ever parked unwoken.

The host predicates are frozen once per drain by memoising them. `admit` couples admission to that freeze: a participant whose congestion windows refused a request counts as full for the rest of the drain, under the reason that refused it. The fold is unconditional, which costs the waiters behind a large request a host that might have taken a smaller one; leaving the participant available costs more, because the same ill-fitting waiter is then re-offered on every binding and burns a nonce each time. A predicate nobody set stays nil rather than being wrapped, so a reader can tell "no allowlist" from "an allowlist that refuses everybody".

`sweepExhausted` runs before every binding and drops the waiters no available participant can serve, instantly and without touching the nonce — a busy or chain-blocked pool answers `ErrHostsBusy`, anything else `ErrNoAvailableHost`. An excluded host is not a dead end there: past the stale window `match` spends the nonce on it rather than burning, and dropping the waiter would fail the one request that rescue exists for. `servable` and `match` read the same `blocks` definition and must be exactly as strict as each other: a `servable` that is stricter fails a request `match` would have served, and one that is laxer keeps a waiter queued that every drain can only answer by burning a chain-costed nonce.

### Where the nonce, the slot and the hold are taken

The concurrency slot and the escrow's in-flight hold are taken inside the same `Advance` that commits the nonce. A serve takes both and gives them back together or not at all; a burn takes the hold alone and gives it back whether its commit succeeds or fails, so a retire cannot land mid-commit on either. The slot travels with the assignment, so the release path covers only what never reaches a dispatch. A failed `Advance` answers the whole queue, not just the waiter a serve decision chose — the session could not advance its nonce at all — and a spent deposit is terminal for the escrow rather than for the request, so only the exhaustion notice gets it replaced. An assignment the waiter can no longer accept is given back and burned as abandoned.

See routing.md, "Where the nonce, the slot and the hold are taken".

## The match decision

`match` is pure and total: it reads nothing, mutates nothing, and every path yields exactly one of serve, burn, hold, or decline (rules.md, "1. A committed nonce is always settled"). Filters are keyed by participant, never by slot, so a host a request excluded once can never be re-served to it through a sibling slot of the same validator.

- **hold** declines the bound nonce without committing it, giving a co-arriving compatible waiter a chance before the nonce is burned.
- **decline** gives the nonce back uncommitted: with no live waiter left, a burn would spend a chain-costed nonce on nobody, under a reason that never happened.
- **serve despite exclusion** is the rescue for a waiter whose only remaining host is one it excluded, past the stale window. It is logged, because a nonce was spent on a host the request asked to avoid. See routing.md, "Serving a host the request excluded".
- **burn** names the block that earned it. Participant-only blocks map to their ghost reason through one table rather than two switches.

## The waiter handoff

A waiter's reply channel is buffered so the dispatcher's handoff never blocks on the caller, and `deliver` never blocks anyway: a full or abandoned channel means the caller is gone. `abandon` takes whatever was already handed over under the same mutex that guards delivery — leaving and taking are one step, because an assignment delivered in that same instant holds a committed nonce and a concurrency slot nobody else will give back. A cancelled caller that finds one keeps it, and so does a caller whose waiter the serve had already bound: binding happens in the `Advance` that commits, and `abandon` on a bound waiter waits for the hand-off. The nonce is committed either way, so `Pick` returns the assignment and the race sends it. See routing.md, "The per-escrow dispatcher".

`submitWaiter` never blocks either: a submit arriving before the actor starts, or faster than it drains, must not stall its caller or pin the lifecycle lock. A full queue and a stopped dispatcher are reported apart, because the first is rate-limit class and the second is retryable against a fresh actor.

## Dispatcher lifecycle

The dispatcher's loop goroutine is the sole owner of the waiting queue and of the session. See routing.md, "The per-escrow dispatcher".

- `stop` is idempotent and blocks until the loop has exited. It holds the write lock while closing the stop channel, which keeps `submitWaiter` from landing a waiter in a buffer nobody will ever read.
- `markStopped` shuts the actor down from inside its own goroutine and refuses once a waiter is already in the submit buffer.
- `retire` runs under the registry lock and only for an actor with no outstanding claim. The claim is taken in the same critical section that hands the actor out and released only when `Pick` returns, so an actor cannot retire between the claim and the submit, nor while its caller waits for an assignment, a caller that left after its serve was bound included. The second case matters for metrics: the actor still commits that nonce and records it, and a record after `EscrowRetired` would write back the series the observer had just deleted. A stopped dispatcher is replaced by the next get-or-create, and the claim keeps the replacement alive, so a submit retries at most once more before the registry itself is closed.
- An escrow's actor is reaped after `idleDispatcherGrace` with an empty queue. Retirement is announced so an observer can forget the escrow: ids are monotonic chain identifiers and are never reused, so a per-escrow metric series that outlives its escrow grows with uptime and nothing else. See routing.md, "Idle dispatchers are reaped".
- **The divergence block and the spent replay outlive the dispatcher.** A dispatcher is recreated for the same escrow on the next request, and dropping either would hand a host that cannot follow this escrow's chain a fresh replay for having been quiet five minutes. What is kept is one entry per escrow that ever saw a divergent host, held for the life of the process.

## Divergence and the replay credit

A participant gets one catch-up replay per escrow before a state-root divergence blocks it there. A host rolls its diff back when its root disagrees, so its state survives the refusal intact and replaying the retained chain costs one request to try.

The credit is returned to a participant that served since it was taken — but only for a send that *started* after the credit was taken. Requests to one participant overlap, so a send that started before the replay proves nothing about the replayed state.

## Failure vocabulary

| Error | What it means | Does waiting help? |
| --- | --- | --- |
| `ErrNoAvailableHost` | no participant can take the request: outside the allowlist, ejected, state-blocked, or the drain's burn budget tripped | yes |
| `ErrHostsBusy` | every host is unusable right now for a reason that clears on its own: windows full, cut off, or owing proof-of-compute — distinct from a host excluded or taken out of routing, and a client retries the two differently | yes |
| `ErrAllowlistUnreachable` | no escrow this gateway serves holds a participant the allowlist admits; the operator narrowed routing to participants none of these escrow groups contains | no |
| `ErrNoEscrowCapacity` | every candidate escrow is at zero spare weight; it names no host | — |
| `EscrowsOutOfFundsError` | every escrow the walk asked refused to pay for this request; it counts them and wraps both `ErrNoEscrowCapacity` and `ErrInsufficientBalance` | a replacement escrow or a settling inference |
| `ErrEscrowBusy` | every candidate escrow's dispatch queue is full (a pinned request: its escrow's): the escrows are sound, the caller arrived faster than they can serve | yes |
| `ErrDispatcherStopped` | the escrow's dispatcher shut down before the request was assigned a nonce; retryable | yes |
| `ErrEscrowGone` | a request's pinned escrow no longer accepts new inferences | no |
| `ErrEscrowAdvanceFailed` | the escrow's session could not advance its nonce for a reason of its own, such as a storage write; every waiter in its queue gets it, and an unpinned request is routed once more to another escrow | yes |
| `SpentNonceError` | the escrow committed a nonce for this request and then failed to prepare it; it names the nonce and wraps the session's error, and the nonce is burned as `nonce_spent_on_failed_prepare` | yes |
| `ErrPinnedEscrowShort` | a pinned pick (a hedge, a retry, or a client naming the escrow) its escrow's balance cannot pay; it wraps `ErrNoEscrowCapacity` and `ErrInsufficientBalance` and never marks the escrow | a settling inference |

## The boundary types

The session adapter lives outside this package, so the two sides meet on a narrow contract.

- `session.Advance` is the one atomic nonce-peek → decide → commit unit: it computes the next candidate binding, calls the decision function, and commits only if the returned intent says to. A declined nonce is left untouched and yields a nil `Prepared`.
- `NonceIntent` exists because that adapter cannot branch on `Decision`, whose variants are unexported.
- `RequestProfile.Params` is forwarded to `Advance` unread and committed there as the escrow's inference params, so it must be exactly `devshard/user.InferenceParams` — not the request body it was built from, which the adapter cannot commit and will reject. `NonceIntent.Params` carries it verbatim under the same requirement, and is set only for a non-ghost commit.
- `RequestProfile.RequestID` is carried only so a burn can name it: `Burn.RequestID` is the request a refused slot was meant for, otherwise the oldest request still waiting when a drain burned the nonce, or the request whose assignment arrived after it left. Routing never reads it.
- `Prepared` is satisfied verbatim by `*user.PreparedInference`, so the api adapter needs no conversion code.
- `Assignment.EscrowHold` gives back the escrow's in-flight count the commit took. It is idempotent, and nil when the escrow source counts nothing. A caller that has taken its own hold releases this one as soon as it has one; a caller that never dispatches releases it instead of dispatching.
- `Escrow.Hold` is taken with the nonce commit and refused once the escrow has been retired. `Candidates` returns escrows in a stable order, already filtered to accepts-new-inferences.
- On `hostLimiter`, `Acquire` is the admission authority and runs with the commit; `Admits` is only a cheap peek that names which rung would refuse, so a match-wait answer from it costs nothing. A slot handed to a caller is released by the engine that spends it, never here.
- On `hostHealth`, `Ejected` is already capped to a fraction of the model's known hosts, so honouring it here can never empty the pool.
- The PoC-preserved set is read per drain, preferring the model's own; a nil set means it has not loaded yet, so every participant counts as preserved. See rules.md, "8. Fail-closed and fail-open are chosen per signal".
- `AffinityHint` is the per-request handle the boundary carries for KV-cache affinity. It holds nothing and no decision reads it.

## Read next

- [`docs/routing.md`](../docs/routing.md) — the drain, the gates, the hold, and every burn reason.
