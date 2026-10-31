# Devshard gateway — capacity and host health

Three separate questions, three separate mechanisms:

| Question | Owner |
|---|---|
| How many requests may the *gateway* have in flight, and for which model? | `limits.GatewayLimiter` |
| How many input and output tokens may *this participant* hold in flight? | `limits.ParticipantLimiter` |
| Is this participant an outlier, and what can it not serve? | `perf.Tracker` |

They are not one thing. The first protects the gateway and respects the network's view of how much of the model's capacity this gateway commands; the second protects the participant from the gateway; the third is an outlier detector plus the sticky record of what a host has proved it cannot do.

A host is removed from a pick by the participant limiter or by the ejection verdict — see [Outlier ejection](#outlier-ejection) for what the ejection verdict is capped by before routing honours it.

The design principle across all three is **adaptation instead of punishment**: an overloaded host receives less traffic within one round trip and recovers on its own, and the longest it is held out of rotation is minutes rather than a sentence it has to sit out ([rules.md](./rules.md), "Not provided").

## Capacity: what the chain says this gateway may use

`limits.Capacity` holds the latest chain snapshot and the escrow membership pushed by the registry, and answers two questions.

**Scale factor for a model** — the fraction of the network's reference weight for that model that is currently available to this gateway:

```
scaleFactor(model) = clamp( Σ availableCurrentWeight / Σ fullWeight , 0, 1 )
```

with three fixed answers at the edges (`limits/capacity.go`, `Capacity.ModelWeights`; `limits/weights.go`, `scaleFactor`):

- Requests blocked by the chain phase → **0**.
- The model is not served → **0**.
- No reference weight observed at all → **1.0**. An unobserved baseline means unlimited, not zero.

**Escrow weight** — how much serving weight one escrow commands for a model:

```
escrowWeight(escrow, model) = Σ_participant currentWeight[p] × hostShare[p] × available(p)
```

`hostShare` is a **normalised share**, `slots(p, this escrow) / slots(p, all live escrows)`, computed by the registry (`registry/membership.go`, `hostShares`). The type is a plain float map, so nothing stops a caller passing raw slot counts — and doing so type-checks, works, and silently gives a participant serving three escrows its full weight in each, tripling its apparent capacity so the gateway over-admits. The invariant lives only in this prose; no type enforces it.

`available` is the participant limiter's non-mutating peek, so congestion-window and cut-off state feed *back* into capacity weighting and escrow scoring (`app/compose.go`, `compose`, which hands `ParticipantLimiter.Available` to `limits.NewCapacity`).

### Two fail-safes with opposite directions

**Unknown model fails closed.** A model absent from a *populated* per-model weight view is served by nobody and gets zero — it does not inherit the all-model view (`limits/capacity.go`, `Capacity.modelServedLocked`). With no per-model view at all (cold start, or an older chain response), the generic view still applies to everything, and the two sides fall back independently so a missing full-weight entry does not suppress a present current-weight one (`limits/capacity.go`, `Capacity.currentWeightsLocked` and `Capacity.fullWeightsLocked`). This matters because `model` is unvalidated client input: without the guard an unrecognised model string inherits full network capacity.

**Unobserved weights fail open.** When neither weight view has any key at all, escrow scoring falls back to the availability-filtered membership share instead of zero (`limits/capacity.go`, `Capacity.EscrowWeight`). Zero would make every escrow score as unusable, so every request in the boot window would be refused. The test is *emptiness*, not staleness: a chain-reported participant is a key in the view whatever its weight, so an empty view means the chain named nobody.

That fallback serves requests correctly and silently, which is its own hazard — an operator sees traffic flowing and cannot tell routing is running blind. So the `WeightsUnobserved` flag of `Capacity.ModelWeights` is derived from the *same two predicates the fallback branch itself reads* and published as `devshard_gateway_capacity_weights_unobserved_by_model`. A gauge computed from the same predicates cannot drift from the behaviour it reports.

## The gateway limiter

Two dimensions — in-flight requests and in-flight input tokens — at two scopes.

**The configured maxima are each model's own budget.** Two models under `max_concurrent_requests: 512` get 512 each, not 512 between them, and a per-model override replaces the configured maximum for its model rather than narrowing it further (`limits/gateway.go`, `GatewayLimiter.admissionFor`). This is forced by the dimension the cap is derived from: the effective limit below comes from *one model's* chain weight, so charging it against another model's in-flight count would make the enforced cap depend on which model's request happened to arrive first. Nothing here bounds the process across models — the escrow registry does, by rejecting an unserved model before it can reach the limiter (`api/routes.go`, `Server.chat` and `Server.routableModel`), so the model set is the operator's and not the client's, and cycling model strings mints nothing.

**Effective limits come from weight when weight is known** (`limits/gateway.go`, `effectiveConcurrencyLimit`). With a per-10 000-weight rate configured and a baseline weight observed, the configured absolute maximum is ignored and the limit becomes the minimum of the current-weight-derived and baseline-derived caps — current weight can lower the cap, never lift it above the baseline. **The room the hosts themselves have is a floor under that**: the limit is never less than the requests their congestion windows can take between them right now (`limits/report.go`, `ParticipantLimiter.ModelConcurrency`). Both are priced from the same figure, so a cold fleet's door is exactly the sum of its hosts' windows; what the two differ by is growth, and a door that stayed at the cold number would leave a request queueing in front of a host with room. When no weight is known at all, the hosts' room is the whole answer. A configured absolute maximum stays a ceiling over either, because the windows that lift them grow without one. Otherwise the configured maximum is scaled by the scale factor, **rounded to nearest**: flooring would take a configured cap of 1 to 0 at any partial capacity, refusing every request for that model instead of degrading it. A NaN or out-of-range scale clamps to **zero**, never to one: a corrupted scale must not grant unlimited capacity (`limits/gateway.go`, `scaleClamp` and `clampUnit`).

A scale factor of zero therefore means an immediate 429 rather than a queue — which is what happens while the chain is blocking requests, if a request somehow reaches the limiter at all.

The capacity values are passed **in** to each acquire and never re-read from a snapshot inside the limiter, and the contract on that parameter is that they are already availability- and block-filtered (`limits/gateway.go`, `ModelCapacity`). Only the composition root satisfies it.

### The queue

If the request does not fit, it joins a queue in arrival order and its own goroutine parks in a `select` on a channel and a timer. There is no per-waiter goroutine and no broadcast on release: a releasing request decrements the counters **and hands the freed capacity directly to the queued waiter, inside the same lock hold** (`limits/gateway.go`, `waiter` and `GatewayLimiter.ReleaseForModel`). Admission is transferred, not re-contested, so an admitted waiter never competes with newly arriving requests.

Waking every waiter on each release starves the oldest: under measurement one waiter is overtaken 1.67 million times in 1.2 seconds and times out into a 429 while newer arrivals pass it.

The promotion sweep walks the queue in arrival order and skips — rather than stops at — a waiter its own model still cannot serve (`limits/gateway.go`, `GatewayLimiter.promoteLocked`). A saturated model therefore cannot stall the others, which is the whole point of the budgets being per model. Within a model it is not first-come-first-served: a waiter whose input tokens do not fit is passed by a smaller request that does, queued behind it or just arriving, which keeps the model's throughput up. Passing has a ceiling, so a large request is never starved by a stream of small ones: once the model's head has been passed 64 times or has waited half its admission wait, no request of that model passes it until it is admitted, and only from then on does the head block the model behind its own tokens (`limits/gateway.go`, `maxQueueHeadPasses`, `queueHeadPatienceDivisor`, `GatewayLimiter.passQueueHeadLocked`).

A new arrival that fits may pass a queued waiter without unfairness, because a queued waiter is by definition one that current capacity cannot serve.

Both races at the edges are handled explicitly. If the timer fires but the waiter was already promoted, the acquire *succeeds* — the slot is already held, and refusing it would leak the transfer. If the context is cancelled but the waiter was already promoted, the slot was handed to a caller that is gone, so it is released (`limits/gateway.go`, the `select` in `GatewayLimiter.AcquireForModel`).

`Reconfigure` swaps the caps and sweeps the queue, so a widened limit reaches a waiting request immediately rather than at the next release.

## The participant limiter: IOCW

State is per `{participant, model}`, and each pair carries two congestion windows rather than one: the input congestion window (ICW) over the tokens an attempt must prefill, and the output congestion window (OCW) over the tokens it reserved for its answer (`limits/host_state.go`, `hostState`). Both are taken at admission and given back together when the attempt ends. A reservation that drained as its tokens arrived would make a host look freer the longer it generates, which is exactly the host with least left to give.

**A window is sized by the weight the host earned and counted in tokens.** What one request is worth comes from the model: its context length on the input side, its output cap on the other (`limits/pricing.go`, `boundsIn` and `ParticipantLimiter.windowsForLocked`). The race compares a host's context refusal against a different number, the `--max-model-len` hosts run with ([race.md](./race.md), "Escalation"). A context length is resolved in three steps — what governance reports as `Model.context_window` (read by `chain.Reader.Models` and carried on `PhaseSnapshot.Models`), then the operator's pin `model_limits[m].max_model_len`, then `fallback_max_model_len`. Governance comes first because it moves before an operator could react: a pin that outranked it would keep pricing a model at a length the chain has already changed. Only governance's latest reply outranks the pin: a length it has stopped naming ranks below the pin and above `fallback_max_model_len` (`limits/pricing.go`, `ParticipantLimiter.windowsForLocked`), which is the order the race and the balance floor read too (`config.Limits.ContextLength`). A context window above a hundred million tokens is not a unit a window could be priced in, so governance reporting one is dropped rather than clamped and that model keeps the length it was last priced at (`app/capacity.go`, `contextWindowsOf`), while an operator configuring one is refused (`config/validate.go`, `Config.Validate`). The output side takes `model_limits[m].max_tokens_cap` and falls back to the global `max_tokens_cap`. The additive step is that same unit — one request of that model — so there is no step to tune.

**No output budget passes `filters.MaxOutputTokens`, ten million.** Every path through `capOutputTokens` clamps to it, the admin bypass included, and `Validate` refuses a `max_tokens_cap`, a `default_max_tokens` or a per-model override above it (`filters/rules_tokens.go`, `capOutputTokens`; `config/validate.go`, `Config.Validate`). A request that cannot ask for more than the ceiling is a request whose price, and the window priced from it, stay inside the arithmetic that counts them ([rules.md](./rules.md), "10. Counters saturate").

**A host with nothing in flight always admits one request, whatever its size** (`limits/participant.go`, `admissionLocked`). Without that rule a prompt larger than the whole window is a request no host could ever serve. With work already in flight both dimensions have to fit, a half-open probe is one request whatever its size, and an open cut-off admits nothing at all.

That rule is a floor under the arithmetic rather than the common case, because a window priced in a model's context length is wide. **The output side is the one that binds.** A request is priced there at the model's output cap, and the gateway hands a client that asks for nothing the same cap by default, so the output window's size in tokens is exactly a count of concurrent requests.

### What a host's weight buys

The chain already says how much compute each host brought: its weight, per model, refreshed every epoch and cut when it spends that compute on proof-of-compute instead. The gateway-wide cap is priced from it — `requests_per_10000_weight` turns the fleet's weight into the requests it may run at once (`limits/gateway.go`, `effectiveConcurrencyLimit`). **The same figure prices each host's own windows**, so the two levels agree by construction instead of contradicting each other: the sum of what every host is allowed is what the fleet is allowed.

```
concurrency = weight × requests_per_10000_weight / 10000     (never below one request)
ICW floor   = concurrency × the model's context length       starting size: twice the floor
OCW floor   = concurrency × the model's output cap           starting size: the floor
```

The asymmetry is what a request actually costs. It reserves its whole output budget, and almost always uses it, so the output window is exactly the concurrency the weight bought. It almost never fills the context, so the input window opens at twice what that concurrency would reserve and only falls to the floor when a run of bad answers takes it there.

Neither number is a ceiling: a host that keeps answering grows past both, and one that struggles is narrowed back to what its weight bought and no further. Because the fleet's own door takes the hosts' room as its floor, that growth is admission the gateway can actually use rather than capacity that queues behind a cap priced for a cold fleet. A host the chain has no weight for keeps the flat `host_input_window`/`host_output_window` request counts (`limits/pricing.go`, `windowsForLocked`). Weights arrive on the phase snapshot the capacity model already watches, taking the lower of the chain's current and full views so a current weight above the full one cannot buy a wider window (`app/capacity.go`, `participantWeightsOf`).

**A window follows its floor up and its starting size down, and only down when that size actually fell** (`limits/participant.go`, `ParticipantLimiter.ObserveWeights`). Both halves are load-bearing and neither is the plain clamp it looks like. The snapshot lands every few seconds whether or not the chain said anything new, so re-basing every open window on each one would take back within a poll interval everything a host answered its way into. And a weight that moves *up* raises the starting size past nothing: a host that grew above what its old weight started at would be reset by the very snapshot that says it earned more, which is the ceiling this section denies, arriving through the back door. What the section promises — narrowed back to what its weight bought and no further — is the floor, and the floor is followed unconditionally.

The requests a weight buys are rounded, not truncated. Truncation discards everything under a whole request, always downwards, and the discard is regressive: a weight buying 1.68 requests would keep one and lose two fifths of what it earned, where one buying 23.9 loses a twenty-fifth (`limits/pricing.go`, `earnedConcurrency`). The fleet's own copy of the arithmetic still floors (`limits/weights.go`, `weightConcurrencyLimit`), so the two levels agree on the weight and not on the last fraction of a request; the door takes the hosts' room as its floor, so the host side rounding up lifts the door rather than contradicting it, and the configured maximum still caps the result.

Pricing a window from weight bites where a single request is priced above the whole window — a prompt longer than the context length its model is priced at, or a window a run of bad answers has left at its floor.

**`Acquire` returns a lease**: a closure that gives back exactly what it took, exactly once (`limits/participant.go`, `ParticipantLimiter.releaseFor`). The lease travels to the attempt on `scheduler.Assignment.HostSlot` and is released where the attempt ends, the same shape as the escrow hold beside it (`scheduler/contract.go`, `Assignment.ReleaseHostSlot`). The engine's view of the limiter can neither acquire nor release, so the pairing is not something a call site can get wrong.

**One answer buys one whole request of room.** A successful answer widens the input window by one context length and the output window by one output cap — the same unit each window is priced in, whatever the answer itself measured (`limits/congestion.go`, `grownBy`). A window widens only when its peak since the last adjustment reached half of it, so an idle host accumulates nothing (`limits/window.go`, `window.growIfEarned`). A host serving eight at once therefore climbs eight rungs in the time it takes to answer them, and one serving one climbs one. **There is no ceiling** — where a window stops is what the host's congestion signals say, not a number an operator guessed.

**Narrowing is one ladder of four factors**: soft `0.85`, hard `0.70`, severe `0.50`, and `0.90` on the window the signal did *not* blame, because prefill and decode share one device and a host struggling at one end has less to give at the other (`limits/congestion.go`, `CongestionFactors.narrowingFor`). A signal that blames the host as a whole applies its own factor to both windows and carries no cross factor. Nothing goes below the floor, which is what the host's weight bought. Any factor other than one starts that window's peak again from what is in flight, including a narrowing that finds the window already at its floor (`limits/reaction.go`, `ParticipantLimiter.narrowLocked`; `limits/window.go`, `window.narrow`).

What each answer blames, and what it does to the cut-off (`limits/congestion.go`, `responseFor`):

| The attempt ended as | Input window | Output window | Cut-off |
|---|---|---|---|
| a healthy answer, with the delay signal quiet | grows | grows | cleared, and a half-open probe recovers |
| a healthy answer, with the delay signal naming one dimension | soft when it names input, cross when it names output | soft when it names output, cross when it names input | cleared, and a half-open probe recovers |
| a healthy answer, with the delay signal naming both | soft | soft | cleared, and a half-open probe recovers |
| a late answer, from a host that missed a deadline | untouched | untouched | cleared, and a half-open probe recovers |
| `429` or `503` | soft | soft | counts when the host carried nothing else of ours for this model as the refusal landed, untouched on the race's report |
| a burn-empty the host held past the refusal timeout | soft | soft | counts when the host carried nothing else of ours for this model as the refusal landed, untouched on the race's report |
| an attempt the twenty-minute backstop cut | soft | soft | counts when the host carried nothing else of ours for this model as the refusal landed, untouched on the race's report |
| an upstream `5xx`, or an empty answer whose nonce closed | hard | hard | untouched |
| an empty answer that left its nonce open | severe | severe | counts |
| a missed receipt deadline | severe | severe | untouched |
| a missed first-token deadline | severe | cross | untouched |
| a stream that went silent between chunks | cross | severe | untouched |
| a transport fault — a dial failure, a `403`, a clock drift | untouched | untouched | counts |
| a model outcome — a burn-empty inside the refusal timeout, an error stream, a capability refusal | untouched | untouched | untouched |

A receipt arrives before any prefill, so a receipt deadline a host missed says nothing about which half of it is slow and narrows both. A first token is what prefill produces and a silence mid-stream is what decode failed to, so each of those blames its own window and touches the other through the cross factor. A silence is the one severe signal the cut-off ignores: a host answering slowly is not the host the cut-off is for, and one that stalls again and again is the outlier detector's to withhold. A transport fault is the mirror image — it moves no window at all, because it says the host is broken rather than full, and a broken host is exactly the cut-off's business. A model outcome moves nothing and creates no state: it is what the model produced, not what the host failed to carry, and narrowing for it would penalise the wrong party (`limits/congestion.go`, `response.inert`).

Two of the rows above are the ladder reaching past the terminal an attempt carries (`engine/judgement.go`, `RaceOutcome.Verdict`). A burn-empty is the model's own output and moves nothing, until the host has held the request past the chain's refusal timeout and still returned nothing, which is a host that took on more than it could carry. An attempt still running when the twenty-minute backstop cuts it says the same thing, and both report the overload a `429` reports.

**The delay signal narrows a host before anything has failed.** `perf` keeps 64-sample p75 rings for first content and for time per output token, and a baseline per ring: the best p75 the host has held (`perf/host.go`, `latencyWindow`). `Tracker.Pressure` is the current p75 over that baseline, and a healthy answer from a host more than `host_congestion_slack` above it is soft congestion on the dimension it names rather than growth (`limits/reaction.go`, `ParticipantLimiter.OnResult`; `limits/congestion.go`, `Pressure.congestedDimension`). One congested dimension moves both windows, like any other signal that blames one: the named window takes the soft factor and the other takes the cross factor, and neither grows, because an answer that arrived late for its host is not an answer that earned it more room. The baseline falls to a new best at once and rises a thousandth of the gap per sample, an order of magnitude slower than the ring fills: a baseline that kept up with the ring would track the latency the host currently holds, and a host degrading steadily would never read as congested.

**The cut-off** trips after `host_cutoff_after_failures` consecutive faults of the kinds that count towards it — a transport fault, an empty answer that left its nonce open, and an overload from a host that carried nothing else of ours for this model when it refused — or immediately when a half-open probe fails, because a probe gets exactly one try. An overload is split on what the host was carrying at the moment it refused, which the race reads live rather than at its report, because by the time a race reports the host's other requests may have ended or new ones landed: the coordinator calls `CountRefusalIfIdle` as it handles the attempt's `AttemptDone` (`engine/report.go`, `raceCoordinator.countRefusal`). One that refused while this gateway had nothing else on it for this model is refusing work rather than full of ours, so it counts, and every host answering `503` stops costing a nonce per request once its cut-off opens; one still carrying other requests of ours is busy, so its refusal leaves the count where it was rather than clearing a streak of faults it did nothing to end. The race's report then narrows the windows and leaves the count alone, so no refusal is counted twice (`limits/reaction.go`, `ParticipantLimiter.CountRefusalIfIdle` and `ParticipantLimiter.OnResult`). Backoff is `base × 1.6^count` clamped to the maximum, with up to 20% jitter added after the clamp so hosts saturated together do not all reopen on the same tick; the count stops rising once saturated so the exponent cannot overflow the duration (`limits/reaction.go`, `ParticipantLimiter.applyBreakerLocked`). The jitter is gRPC's connection-backoff constant.

Recovery walks the ladder back down: a healthy answer while half-open clears the trip *and* decrements the backoff count. The subtle part is that it must clear the open-until timestamp and not just the probe flag — any host with a non-zero open-until reads as half-open, so clearing only the flag pins a recovered host at one probe forever.

**`Admits` is a peek, not an authority.** It evaluates the same predicate against the smallest possible request, without mutating in flight, without setting the half-open flag and without creating state for an unseen participant (`limits/participant.go`, `ParticipantLimiter.Admits`). An unseen participant answers `open` without building a throwaway state, because a fresh state is never cut off and carries nothing. The peek holds the limiter's lock for reading only, so concurrent picks do not queue behind each other, only behind an acquire, a release or a verdict. It answers with `open`, `window_full` or `cut_off` rather than a bare boolean, because routing treats a busy host and a broken one differently: only a full window may be crossed by a forced send. `Available` is the same peek read as a yes or no, which is all the capacity weighting needs. It exists because routing needs a cheap pre-filter where a stale answer costs nothing. Using it *as* the authority is what produced the admission time-of-check-to-time-of-use hole described in [rules.md](./rules.md); the acquire that authorises a send happens inside the same step that commits the nonce.

**`Overdraft` is the same acquire with the window's vote removed.** It takes the tokens whether or not they fit and hands back the same lease, and the only thing that still refuses it is the cut-off — open, or half-open with its probe already in flight (`limits/participant.go`, `take`). The scheduler calls it for one binding at a time, after a run of burns long enough to be worth more than the latency the overdraft costs; the rule that decides when is in [routing.md](./routing.md), "The forced send". The tokens it spends are in flight like any other request's, so the next window decision already sees them and the lease gives them back exactly once.

**The growth gate reads a peak, and that is what makes call order irrelevant.** `Acquire` records each window's in-flight high-water mark at the instant it takes the tokens, where nothing can undo it, and `OnResult` compares that peak — not the live count — against half the window (`limits/window.go`, `window.take`; `limits/reaction.go`, `ParticipantLimiter.growLocked`). This matters because the engine releases an attempt's lease before it announces the attempt is done and reports its verdict afterwards: a gate reading the live count would see the tokens already given back and refuse to widen a window that had been genuinely saturated. Reading the peak decides identically whichever of release and result runs first, so for growth the order of the three calls is an implementation detail and **not** a contract. The live refusal count is the one reading that does depend on order: `runAttempt` gives the lease back before it emits `AttemptDone`, and the coordinator counts a refusal as it handles that event, so the in-flight count `CountRefusalIfIdle` reads is the host's other work for this model at the moment of the refusal (`engine/attempt.go`, `runAttempt`).

**Manual reset.** `POST /v1/admin/participants/unquarantine` clears every model's cut-off for one participant, restores both windows to their initial size and puts them back in slow start, and reports "not found" for a participant the gateway is not tracking (`limits/window.go`, `window.reopen`). In-flight counts are left alone: they count attempts still running, not penalty.

Both limiters take a settings change without a restart. The gateway limiter swaps its whole configuration; the participant limiter keeps what each host has earned. Governance naming a model's context length moves the bounds a host is judged against and lifts a window to the new floor, but never returns a window a run of bad answers took away (`limits/participant.go`, `ParticipantLimiter.ObserveModels`). A governance reply that names no model is no observation rather than a chain that named nobody: the read is not counted as fetched, each model keeps the length it was last priced at, and an observation that does arrive is merged into what is already known (`chain/observer.go`, `PhaseObserver.fetchModels`). The direction is load-bearing because the floor only ever rises — one empty reply read as an answer would reprice every model at `fallback_max_model_len` and leave every host's input window that wide for the life of the process. An operator raising the initial window is the deliberate act, and it does lift a window that collapsed below it (`limits/participant.go`, `ParticipantLimiter.Reconfigure`) — a knob that reached only a restarted process would be useless exactly when it is reached for. Any other settings write, one that leaves the initial size where it was, keeps every narrowed window narrowed and lifts it only to the floor: an unrelated admin call during an incident must not reopen the hosts the gateway just backed off from. Nothing is lost by being generous: a host that is still failing narrows again within seconds, and the cut-off is what protects against one failing badly.

Both admission defaults are sized against the gap between arrivals rather than against a round trip. The hold grace exists because a nonce bound to a host no waiting request can use burns before the next request reaches it, so the grace has to outlast that gap. Queue depth is bounded the same way: a waiter that provably cannot reach the front inside the wait budget is refused on arrival rather than made to wait it out.

## The balance floor

An escrow that runs to zero does not stop serving — it starts failing. A depleted escrow left in selection fails every request routing hands it: three of them produce 178 `insufficient escrow balance` errors in a minute, over half of a day's client-facing failures.

The floor prices what an escrow must hold to be `full` (the guarantee) and the creation floor, and retires nothing. It reads the balance alone and never the requests already on the escrow. The session's balance is net of what every started request reserved (`devshard/state/machine.go`, `applyStartInference`), and every escrow hold that counts a request is taken inside the `Advance` that commits its reservation or after it (`scheduler/dispatcher_queue.go`, `drain`; `engine/race.go`, `begin`), so nothing a hold counts is still to be charged. Counting the holds again charged every request on the escrow twice, and because a race keeps its hold until its vote is posted, minutes after its answer, that refused arrivals the balance could pay and refused a race's own hedge on account of the race itself. There is nothing to tune: routing prices the request the way the chain does, `(input_length_bytes + max_tokens) × token_price + fee_per_nonce`, reading both prices off the escrow's own session. A token price of zero leaves only the nonce fee, and with no fee either the escrow is never priced on this rule: a floor sized in the wrong unit would misprice every escrow at once, so the gateway declines to guess.

**Both sides are priced in the units the chain charges them in, and the units are the whole point.** The chain reserves `(InputLength + MaxTokens) × token_price` (`devshard/state/machine.go`, `applyStartInference`) and then takes `fee_per_nonce` for the nonce that carries it, refusing the whole nonce when the balance left cannot pay the fee (`devshard/state/machine.go`, `applyCore`). A floor that prices either term differently is wrong in the one direction that does not show up anywhere: it lets through exactly the arrivals it exists to stop, silently, because nothing downstream compares the two numbers.

- **The input side is the body's bytes.** `InputLength` is the length of the normalised body, while the gateway's own input number is the request's prompt token count, the one the congestion windows are denominated in: the pinned tokenizer's count when the request was tokenized, or that same length divided by four otherwise ([`request.md`](request.md), "The prompt token count"). Pricing the floor in that count budgets a fraction of the input term: on an 8 KB body the chain reserves 8 192 for the prompt and the floor would budget about 2 048. The request profile therefore carries both numbers, named for what each one is (`scheduler/contract.go`, `RequestProfile`, `InputTokens` and `InputBytes`).
- **The nonce fee is charged on top.** A balance that covers the reserve but not the fee is refused just the same, so every price the floor takes — the request, the escrow's own floor — adds one fee per answer (`scheduler/escrow_pick.go`, `requestCost`). A burned nonce pays the fee too; the floor does not price the burns a pick forecasts.
- **The output side is what this request reserved.** `MaxTokens` is the number the chain is handed, and it can sit far above `max_tokens_cap`, because a per-model cap replaces the global one and an admin request skips the cap entirely (`filters/rules_tokens.go`, `capOutputTokens`).

**Two prices, because the floor is asked two different questions** (`scheduler/gates.go`, `requestReserve` and `retirementReserve`).

*Can this escrow pay for this request?* is a fact about the request, priced exactly as above. An escrow that cannot is skipped for this arrival and the caller is answered `ErrInsufficientBalance`. Nothing else follows: the escrow serves the next ordinary request unchanged.

*Is this escrow finished?* is a fact about the escrow, and it is the question the funding planner prices `full` and the creation floor on. So it reads nothing from the arriving request, whichever of the two the tick ends up choosing, and is priced on the escrow's model: its floor is the model's context length — the `--max-model-len` the chain registers, else the operator's `model_limits[m].max_model_len`, the same length the race judges a context refusal against ([race.md](./race.md), "Escalation"; `config.Limits.ContextLength`) — counted as prompt bytes at the gateway's standing estimate of `filters.PromptBytesPerToken` (4), because the chain reserves a prompt by its bytes, and never less than one capped answer, which is all a model with no known length is priced at (`config.Limits.RetirementReserve`; `scheduler/gates.go`, `retirementReserve`). Nothing is added for load: the balance a session reports is already net of what its started requests reserved, so an escrow is full when that balance pays one whole race at the floor, however busy it is (`scheduler/escrow_pick.go`, `affordsAttempts`; `liquidity.Classify`). Pricing it on the request instead lets a single arrival take an escrow that is perfectly solvent out of routing, and because the pick walks every candidate against the same price, one oversized arrival takes *every escrow of the model* out of routing in a single pick. Both terms of a request price are unbounded in the way that matters: a body reaches the ingest cap, and the chain counts that body's **bytes**, while an admin request's `max_tokens` reaches ten million.

**A floor of one capped answer strands an escrow.** A balance that covers a capped answer but not a long-context request refuses every long request with `ErrInsufficientBalance` and is never retired by routing, so it stays in routing answering 503 until the planner replaces it, fees wear it down or the epoch rotates it ([gonka-ai/gonka#1894](https://github.com/gonka-ai/gonka/pull/1894)). The model's context length as prompt bytes closes most of that band but not all of it: four bytes per token is an estimate, not a bound, so a prompt whose text runs denser in bytes — non-Latin scripts, heavy JSON escaping — can still cost more than the floor near the context length, and an escrow holding less than that refuses it without routing retiring it. A request past the context length is refused by the hosts themselves. The floor is per model, so a short-context model needs a fraction of what a long-context one does.

The same split governs a nonce the session refused to compose. `Advance` answering `ErrInsufficientBalance` prices *that request*, so the dispatcher marks the escrow spent only when it also cannot cover the escrow's own floor (`scheduler/dispatcher.go`, `escrowIsSpent`), and the tick drops that mark and counts it in `devshard_gateway_planner_ignored_marks_total` rather than replacing the escrow ([escrows.md](./escrows.md), "Depletion"). One request too dear for an escrow is not the end of its life.

The funding planner keeps `full_context_slots` escrows above this floor (see [`escrow/README.md`](../escrow/README.md), "The funding planner").

## An empty stream has degrees

`empty_stream` is the bottom of the classification ladder: a receipt arrived, no error came, and nothing the client could render did either — not `content`, not `reasoning`, not `reasoning_content`, not a tool call. An empty string does not count, because upstream opens every reply with `{"delta":{"content":"","role":"assistant"}}` and crowning on that would hand the client a winner chosen for sending a preamble first.

That one label covers three very different hosts, so the finished-attempt line carries `stream_chunks` and `usage_tokens` whenever it fires:

| stream_chunks | usage_tokens | what it was |
|---|---|---|
| 0 | 0 | nothing after the receipt — the host took the nonce and never wrote a byte |
| > 0 | 0 | events arrived and all of them were empty |
| > 0 | > 0 | the host reported tokens it never delivered |

`stream_chunks` counts every write, not the content-bearing ones, which is what separates the first row from the second. On an empty-answer route (a profile with `empty_answer_counts_tokens`) the third row is classified as `burn_empty` instead, because there the host's own token count is the one signal that separates a model producing nothing from a host carrying nothing.

The distinction matters for what it costs. A host that answers empty after a receipt cannot have its nonce closed early: the timeout vote is only accepted once the chain's execution deadline has passed, around thirty minutes from the receipt, and the escrow's in-flight count carries it the whole time.

## What in-flight actually counts

`devshard_runtime_active_requests` is not the number of clients waiting. The escrow hold a race takes is kept "for as long as the race's vote is owed" (`engine/engine.go`, `raceRegistration.holdEscrow`), and it is released on the goroutine that posts the timeout vote for every nonce the race left unfinished — after the losers have been given their grace, which defaults to ten minutes. So a request whose answer was delivered long ago keeps its escrow's count up until the chain has been told what became of each of its nonces. Reading the gauge as "requests still generating" overstates load by however much settlement is behind.

A retired escrow stays in `Snapshot` until that count reaches zero, reporting `devshard_runtime_active` as 0 while it drains (`registry/registry.go`). Dropping it at retirement instead would hide the most interesting minutes of an escrow's life behind a panel that no longer has a series to draw.

## Outlier ejection

`perf.Tracker` answers two questions in O(1) with no lock: **is this participant withheld from routing** (`Ejected`), and **did the detector want it out at all** (`Degraded`). They differ only by the pool-wide cap, and each has exactly one job.

**`Ejected` is a routing gate.** It is one of the scheduler's seven host gates — outside the allowlist, proof-of-compute-required, windows full, cut off, ejected, state-blocked and excluded by this waiter — so a host it names receives no request while the gate holds, unless `scheduler_unthrottled_participants` names it (routing.md, "Unthrottled participants").

**`Degraded` is why the gate is not the whole story.** The cap below refuses to honour an ejection once too many of a model's hosts are failing at once, which is exactly the moment the gate stops protecting anything: those hosts stay in rotation. `Degraded` reports the verdict *before* the cap, and the race reads it for one decision — a primary the detector wanted out starts its second attempt immediately, under `primary_degraded`, rather than waiting out the receipt or first-token deadline. That hedge is bounded by the attempt budget, so a correlated outage costs at most one extra attempt per request and never an unbounded retry storm.

**What it tracks is health and latency both.** A sample carries participant, model, whether the host was responsive, and two timings: first content and time per output token (`perf/sample.go`, `Sample`). The timings feed two 64-sample rings whose p75 the escalation ladder reads; the health counters are decayed and feed ejection.

**Ejection triggers** (`perf/ejection.go`, `ejectionPolicy.evaluate`): a run of consecutive failures, or a failure rate above the threshold once the decayed volume is large enough. The minimum-volume gate is why a quiet host is not ejected by one bad request.

**The ladder.** Only a *fresh* trigger starts an ejection — an already-ejected host rides out its current timer rather than having it pushed back. Each fresh trigger lengthens the next ejection linearly in the ejection count, capped, and resets the outcome counters so the rate restarts from zero. Outcomes that land while the host is ejected are discarded the same way, whether from attempts in flight when it tripped or from traffic the cap kept routing to it, so a released host's rate is judged only on what it does after its release. The consecutive-failure run is not reset: it spans the ejection, so a host whose first answer after release fails again is ejected at once, and its first success clears it. The count decays back one rung per full healthy window, with the anchor advancing so the ladder cannot unwind faster than that.

**The pool-wide cap.** Envoy's max-ejection-percent applies per model: at most `min(fraction × known hosts, known hosts − minimum available)` ejections are honoured, resolved by ejection count first, most chronic kept out, with the participant key breaking ties.

**Why it is lock-free.** The shape is sized for a per-host, per-admission read: at five hundred hosts a scan under one global mutex costs 2.35 ms and roughly 1 300 lock acquisitions per request. The tracker publishes two atomic maps of keys to expiry times, one capped for routing and one uncapped; a read is an atomic load, a map lookup and a time comparison, with no lock. The cap and the tie-break are resolved once, at rebuild time, and each entry carries its own expiry so ageing out needs no rebuild at all. The rebuild itself is conditional — only when the membership the cap is computed over actually moved (`perf/tracker.go`, `Tracker`'s two published views, `Tracker.RecordSample` and `rebuildEjectedViewLocked`).

Stale host state is swept at most once per tenth of the staleness window, because entries age out over minutes and scanning every host on every sample costs O(hosts) under the global lock for nothing.

**Capability refusals** — an unsupported protocol version, a tool call the build does not implement, a context length it will not take — are counted, and the smallest context a host has admitted to is kept beside them. Nothing here withholds a host from routing: the counts say what to fix and how often it happened, and a refusal that repeats is a build that refuses everything rather than a one-off. Version refusals are keyed by participant, because a protocol version is a property of the build; tool and context refusals are keyed by participant and model.

## Buffered replies have a ceiling of their own

A client that asked for one whole answer is still served over a stream: the gateway forces `stream: true` upstream. The events are folded into the answer as they arrive and the raw stream is dropped, so what such a request holds is the reply being assembled — and the internal fields nobody will see are removed before the merge, not after it, so a client that did not ask for logprobs never accumulates them. `max_buffered_response_bytes` (`GATEWAY_MAX_BUFFERED_RESPONSE_BYTES`, 512 MiB) is the ceiling on every such reply at once.

The per-request cap is separate and much smaller — 32 MiB on one reply as it is assembled, the same constant the streaming path uses to bound an unterminated SSE event (`api/stream.go`, `maxBufferedResponseBytes`). This ceiling is the only bound on the *sum*, and the request limiter does not stand in for it: with `max_concurrent_requests` unset its cap comes from network weight and routinely admits thousands at once, which made the exposure one per-request ceiling times however many requests arrived.

Past the ceiling a request is refused with `503`, the same answer as a shard with no room — the gateway has nothing left to hold, which is not the caller's doing. A ceiling of zero holds nothing back, for a deployment that would rather be killed by the kernel than refuse a request. Lowering it at runtime stops admitting rather than repossessing: what is already held drains on its own.

`devshard_gateway_buffered_response_bytes` is what is held right now. Watch it before choosing a number: the sum is driven by the *typical* reply, and the ceiling only bounds the tail.

## Nothing here is persisted

Every restart starts clean: no ejections, no capability counts, every congestion window at its initial size, every cut-off closed, every decayed counter at zero. Minute-scale backoff self-heals faster than replaying stale penalties is worth. The cost is that a genuinely bad host gets one free window after every deploy.

The one host judgement that *is* persisted is the operator's manual suspicious-host pin, and for the opposite reason — a pin the gateway acts on but forgets on restart is a state an operator cannot see. The store is written before the in-memory copy (`app/operations.go`, `suspiciousHosts.Add`).

Two things the code does not state:

- The participant limiter forgets a `{participant, model}` pair on the window the performance tracker ages hosts out on, `perf_host_staleness_seconds`, but only a pair with nothing in flight and no cut-off still running. The scan runs inside `Acquire`, at most once per tenth of the window, after the pair it is asked about is marked used (`limits/participant.go`, `ParticipantLimiter.forgetIdleLocked`). Giving a lease back marks the pair used as well, so a host whose verdict has not arrived yet is not forgotten between the release and the result it is about to be judged on (`limits/participant.go`, `ParticipantLimiter.release`). A forgotten host that returns starts at the initial windows with a closed cut-off, as it would after a restart.
- Ejection thresholds are re-read from configuration on every sample, so they hot-reload — but a host's decay half-life is captured when the host is first seen, so a changed half-life applies only to hosts seen afterwards.

## Configuration

| Knob | Default | Effect |
|---|---|---|
| `max_concurrent_requests` | 2 048 | Per-model in-flight request cap, scaled by capacity. |
| `max_input_tokens_in_flight` | 0 (unlimited) | Per-model input-token budget, scaled by capacity. |
| `max_concurrent_requests_per_10000_weight` | 5.0 | What one host's weight buys it, and the floor of the fleet's own door; when set with an observed baseline it replaces the absolute cap. |
| `poc_max_concurrent_requests_per_10000_weight` | 10.0 | The same, used while the chain reports requests blocked. Both are refused at load outside 0 to 10 000 000, the bound of `max_concurrent_requests`, and a weight whose price passes the int64 range is held at its largest value rather than converted. |
| `admission_queue_wait_ms` | 300 000 | How long a request waits for a free slot before a 429. The same value is returned as `Retry-After`. |
| `fallback_max_model_len` | 1 000 000 | The context length one request is priced at for a model neither the operator nor governance names one for. It is deliberately generous: an unknown context must not strangle prefill, and a window that opens too wide narrows itself within a round trip while one that opens too narrow burns nonces. |
| `model_limits[].max_model_len` | unset | The operator's pin for one model's context length, used only while governance's latest reply names no usable one for it. |
| `host_input_window_min_requests` / `host_input_window_initial_requests` | 16 / 32 | The input congestion window's floor and starting size, counted in requests of the model and held in tokens. Sixteen requests always fit, however far the window has been narrowed; a host opens at thirty-two and grows for every window's worth of tokens it carries, until its first congestion signal. |
| `host_output_window_min_requests` / `host_output_window_initial_requests` | 16 / 32 | The same two for the output congestion window, where one request is worth the model's output cap rather than its context length. |
| `io_aimd_beta_soft` / `io_aimd_beta_hard` / `io_aimd_beta_severe` | 0.85 / 0.70 / 0.50 | What a narrowing multiplies the blamed window by, one factor per rung of the ladder. Each is above 0 and below 1: a factor of one never narrows, and one of zero closes a window nothing could reopen. |
| `io_aimd_beta_cross` | 0.90 | What the window a signal did not blame takes with it, because prefill and decode share one device. One is the operator saying there is no cross-dimension penalty. |
| `host_congestion_slack` | 0.30 | How far above the best latency it has held a host may drift before a healthy answer counts as soft congestion on that dimension instead of growth. |
| `host_cutoff_after_failures` | 3 | Consecutive faults of the kinds that count towards the cut-off before the host stops receiving requests. |
| `host_cutoff_ms` / `host_cutoff_max_ms` | 5 000 / 60 000 | How long a cut-off host stays out, first time and at most. The maximum must not exceed the performance ejection maximum, so ejection stays the dominant authority. |
| `perf_consecutive_fail_threshold` | 5 | Consecutive-failure ejection trigger. |
| `perf_failure_rate_threshold` / `perf_failure_rate_min_volume` | 0.15 / 20 | Rate-based ejection trigger and its volume gate. |
| `perf_ejection_base_seconds` / `perf_ejection_max_seconds` | 30 / 600 | Ejection duration ladder. |
| `perf_max_ejection_fraction` / `perf_min_available_hosts` | 0.5 / 4 | Pool-wide ejection cap, and the reason the routing gate cannot empty a model's fleet. |
| `perf_host_staleness_seconds` | 3 600 | When an unseen host is forgotten by the performance tracker and the participant limiter, and when its participant-labelled race series are deleted. |
| `GATEWAY_PERF_EWMA_HALFLIFE_SECONDS` | 600 | Half-life of the decayed success and failure counters. |

Every row above is an admin override, changeable at run time without a redeploy. `max_concurrent_requests`, `max_input_tokens_in_flight`, the two `_per_10000_weight` rows, `admission_queue_wait_ms` and the `perf_*` rows also take a `GATEWAY_*` environment variable read at boot; `fallback_max_model_len`, `model_limits` and the window, congestion and cut-off rows are reachable through the admin surface alone.

The default input-token budget of zero means unlimited, which is worth an operator's attention: with million-token contexts it is the only thing between concurrency and memory exhaustion, and the body-size cap does not throttle load.

## The wait budget

A shard with no room should not answer 429. That status means the client exceeded a quota, and a client that ran into the shard's own capacity exceeded nothing — it carries no hint of when to return, so a well-behaved client retries immediately and deepens the shortage it just hit. Those refusals answer 503. The gateway's own limiter is the other case and keeps 429, because there the caller did exceed a quota (`api/errors.go`, `statusForError`, and api/README.md, "Errors and statuses"). Both carry `Retry-After`: the wait already spent when that is known, a default otherwise.

`admission_queue_wait_ms` is the budget a request may spend looking for capacity, not a delay. A queued waiter is promoted the instant a slot frees. The budget has to outlast the tail of a slot hold rather than its median: a budget shorter than the slowest holds cannot reach most waiters before it runs out.

`admission_queue_per_slot` follows from the same ratio: depth is set so a burst is held without admitting waiters the budget provably cannot reach. Waiting the whole budget out and being refused anyway costs the client the wait and the shard the connection, so a request that provably cannot reach the front in time is refused on arrival instead. Depth is counted per model against that model's own concurrency, because the caps are per model: a heavy model does not shrink a light one's queue.

Two gates can refuse, and they answer different questions. The gateway limiter asks whether there is budget — concurrency, input tokens, chain weights. The scheduler asks whether a live host will take it — participant windows, breakers, chain phase. A request can pass the first and stall at the second: one burst answers zero limiter refusals against nine scheduler refusals. The budget bounds the limiter side only; the scheduler refuses immediately rather than waiting.

Escrow membership must reach this layer: without it `EscrowWeight` returns zero for every escrow, escrow selection fails on every request, and the gateway serves nothing while every health check stays green.

## Nonce dispositions

Settlement credits each slot with `assigned_nonces - protocol_misses` completed inferences, whatever GNK those nonces paid. Three gateway behaviours break that count: policy burns nonces without sending work, a sent request can go unfinished while its timeout never applies, and overscheduling turns one client request into several completed inferences. The nonce ledger exists to say which of those happened and how often; the accounting model is `proposals/gateway-dashboard`.

The disposition model follows that proposal; the event vocabulary is the gateway's own, built from the facts this gateway already produces, which are coarser than the reference's and carry the same information in fewer events.

| what the ledger folds | where it comes from |
|---|---|
| escrow membership, its latest nonce, and what the chain recorded per slot | a ten-second sweep of the published escrow set: `Snapshot` names the escrows, each session's `SnapshotState` carries the slot group, the latest nonce, and `HostStats` |
| a burned nonce and its reason | `tracedDispatches.GhostBurned`, which carries the nonce and one of nine reasons |
| every attempt of one race | `nonceAccountedRaces.RecordRace`: per attempt the nonce, whether it was sent, whether the protocol finished it, and whether the client got its answer |
| a timeout's kind, action and reason | `nonceAccountedRaces.RecordTimeout` |

Three of those are pushed and three are swept. Membership and host stats have no event of their own and change slowly, so reading them on a timer is both simpler and less invasive than a callback on every nonce; a race and a burn are single moments and must be told.

One race outcome replaces six of the reference's events, because it already aggregates what they report separately: a send, a winner, a loser, an unknowable usage, and the finish. One timeout event replaces two.

**A nonce names its own slot.** The reference reads applied diffs to learn which slot spent a nonce. That is unnecessary here: the chain's own convention makes the executor the slot at nonce modulo group size, so the ledger attributes a nonce arithmetically and needs no protocol-transition channel at all.

Two consequences follow from folding coarser events. The reference deduplicates replayed callbacks by using each event as its own map key, which requires every event to be a comparable struct; a race outcome carries a slice of attempts and cannot be one. It does not need to be: a race reports itself once, a burn is recorded once, and swept facts are idempotent by construction because each observation replaces the last. Neither a dedup map nor a comparable event struct is required.

**An unapplied timeout is not a settled one.** Every path out of `user.HandleTimeout` returns an error, including its own success — that error carries "this inference timed out" back to the request. So the error says nothing about whether the vote reached the chain, and `TimeoutResult.Applied` is what the ledger reads instead. The distinction is the whole point: a nonce whose timeout never gathered enough votes still settles as a completed inference for a participant that never answered, and recording it as a posted vote hides exactly that.

**An unfinished nonce is not a verdict, and a received Finish is not a finish.** A nonce is finished in the ledger when the escrow's log holds its Finish: the sweep reads every live record and marks a nonce finished once its record is `Finished`, `Challenged`, `Validated` or `Invalidated`, or when it is sealed away after being read in one of those (`accounting/book.go`, `FinishApplied`). A record can reach a terminal status and be sealed between two sweeps — a validated one seals within twenty nonces — so the sweep also closes every open nonce whose record has left the window: the record the state machine kept at seal time says whether its Finish applied, and the session's own outcome answers when that copy is absent, which is safe for a sealed record because nothing can change it any more (`nonces/sweep.go`, `closeSealed`). The session's own view — a Finish in the mempool that would apply — never finishes a nonce, so one whose Finish is queued, pinned for an error-miss claim or stuck stays in flight where an operator sees it. The protocol can finish a nonce after the race that gave up on it; the next sweep that reads the record moves it out of flight.

**What the ledger cannot see.** A nonce is invisible to it between commitment and the end of the race that spent it, because a race reports only when it ends. Those nonces fall into `unobserved` alongside genuinely protocol-only ones, so that number is a floor on protocol overhead rather than a measurement of it; a baseline that grows while traffic is steady is the signal worth watching. `pending` is the separate case of a nonce seen unfinished whose timeout has not settled, and `overcounted` — classified beyond what the chain assigned — should never be anything but zero.

The gateway's vocabulary is richer where its facts are. Ten ghost reasons, not five: `participant_ejected_no_send`, `participant_outside_allowlist`, `participant_state_diverged_no_send`, `participant_absent_from_chain_no_send`, `request_abandoned_before_dispatch` and `nonce_spent_on_failed_prepare` are first-class rather than an unknown reason, and a host that had no room (`participant_window_full_no_send`) is told apart from one this gateway had already stopped sending to (`participant_cut_off_no_send`), because the two call for opposite answers.
