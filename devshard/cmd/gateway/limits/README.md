# `limits` — who gets admitted, and how many at once

Three limiters, each answering a different question.

## What it owns

- **The gateway limiter** (`gateway.go`) — an admission queue in arrival order over concurrent requests and in-flight input tokens, per model, which a request that fits passes. Refuses with a typed rejection that names which cap turned the request away, so an operator is not left a wall of identical statuses.
- **The participant limiter** (`participant.go`, `pricing.go`, `window.go`, `host_state.go`, `reaction.go`, `report.go`, `congestion.go`) — two congestion windows per `{participant, model}` and a cut-off over them: one window over the input tokens an attempt must prefill, one over the output tokens it reserved. It narrows on host-attributable failures, missed deadlines and latency past the host's own best, widens on answers that arrive in time, and half-opens after a cut-off to admit one real request rather than waiting for a probe. `Acquire` hands back a lease that releases exactly what it took, exactly once.
- **The capacity model** (`capacity.go`, `weights.go`) — scales the caps by the host weight the chain reports for each model, so a shard that has lost half its hosts admits proportionally less.

## Boundaries

- **Zero means unlimited**, for both the concurrency cap and the token budget. With `max_concurrent_requests` at 0 (unset, it is 2 048), the effective cap comes from the per-10 000-weight rate instead.
- **Only host-attributable verdicts move a window.** A model refusal or a burn-empty is what the model produced, not what the host failed to carry, and narrowing for it would penalise the wrong party. An empty answer is the host's: it narrows both windows, and one that left its nonce open narrows them harder and counts towards the cut-off.
- **A busy host and a broken one are different answers.** Congestion narrows a window; a transport fault moves no window at all and counts towards the cut-off instead, because a host that cannot be reached is not a host with no room.
- **A lease is the only way tokens come back.** `Acquire` returns the closure that releases exactly what it took and does so once; the package exposes no release keyed by participant, so nothing can give back what it never took.
- **A corrupted capacity scale fails closed.** A NaN must not be read as unlimited capacity.

## The gateway limiter's queue

A request that no cap can ever admit — one asking for more input tokens than the model's whole budget — is refused immediately rather than queued, because waiting could not help it.

Everything else queues in arrival order, and the queue is not a delay: a waiter is appended and the queue is swept in the same lock acquisition, so capacity that is already free is handed over at once. Sweeping **skips** a waiter its own model still cannot serve rather than stopping at it, so one saturated model does not block a different one behind it. A freed slot is given to the queue directly under the release's own lock, which is why an arriving request never overtakes a waiter the freed capacity serves. It is not first in, first out within a model, though: a waiter whose input tokens do not fit the model's token budget is passed by a smaller request of the same model, arriving or queued behind it, that does. Passing is what keeps the model's throughput up, so it stays, under a ceiling: once the model's head has been passed 64 times (`maxQueueHeadPasses`) or has waited half its admission wait (`queueHeadPatienceDivisor`), no request of that model passes it until it is admitted, so the head is blocked behind its own tokens only past that point and a large request is never starved by a stream of small ones (`gateway.go`, `passQueueHeadLocked` and `starvingLocked`).

Two races at the end of a wait are resolved by whether the waiter is still in the queue. If it was promoted just as its deadline fired, the slot is already its own and it proceeds; if it was promoted for a caller whose context had already been cancelled, the slot is released again rather than leaked.

`AdmissionQueuePerSlot` bounds the queue at that multiple of the model's own concurrency cap; past it the request is refused with `queue_depth` rather than joining a queue it cannot reach the front of within the wait budget.

Releasing leaves an idle model's counter in the map. Deleting it would cost an allocation on the model's next acquire, and a model that goes quiet would vanish from the snapshot — which a reader cannot tell apart from the gateway being gone. The map is bounded by the number of routable models.

## How one model's cap is computed

The configured maxima are **each model's own** budget, not a shared pool, and a per-model override *replaces* the configured maximum rather than narrowing it further.

The concurrency cap is then derived one of three ways:

- When a per-10 000-weight rate and a baseline weight are both present, the cap is `min(limit(currentWeight), limit(baselineWeight))` — the current weight must never lift the cap **above** the baseline-derived one — raised to the concurrency the hosts' own windows have already earned where that is larger, and never above the configured maximum.
- When only the host windows report a concurrency, that is the cap, bounded the same way.
- Otherwise the configured maximum is scaled by the capacity scale factor, rounded to nearest rather than floored, and never above the configured maximum itself.

The input-token cap only ever takes the last path.

## What blames which window

A verdict is read once, into the tier it narrows by, the window it blames and what it does to the cut-off (`congestion.go`, `responseFor`). [`docs/capacity.md`](../docs/capacity.md), "The participant limiter: IOCW" sets the same table beside the factors and the units.

| Verdict | Narrows | Blames | Cut-off |
| --- | --- | --- | --- |
| `Success` | nothing while the delay signal is quiet, soft otherwise | —, or the dimension the delay signal names, with the cross factor on the other | cleared, and a half-open probe recovers |
| `LateSuccess` | nothing | — | cleared, and a half-open probe recovers |
| `Overload` | soft | both | counts when the host carried nothing else of ours for this model as the refusal landed, untouched on the race's report |
| `UpstreamFault`, `EmptyAnswer` | hard | both | untouched |
| `EmptyAnswerLeftOpen` | severe | both | counts |
| `MissedReceiptDeadline` | severe | both | untouched |
| `MissedFirstTokenDeadline` | severe | input, cross on output | untouched |
| `DecodeStalled` | severe | output, cross on input | untouched |
| `TransportFault` | nothing | — | counts |
| `ModelOutcome` | nothing | — | untouched |

- **Every signal that narrows moves both windows.** One that blames a single dimension applies its tier there and the cross factor to the other, because prefill and decode share one device; one that blames the host as a whole applies its tier to both and carries no cross factor, having nothing left to spread (`congestion.go`, `CongestionFactors.narrowingFor`).
- **A receipt is owed before any prefill**, so a missed receipt deadline says nothing about which half of the host is slow and narrows both; a first token is what prefill produces and a mid-stream silence is what decode failed to, so those two blame one window each.
- **A verdict that moves nothing returns before the lock.** `ModelOutcome` neither narrows nor touches the cut-off, so it takes no lock and creates no state for a pair (`congestion.go`, `response.inert`).
- **Three verdicts feed the cut-off**: `TransportFault`, which moves no window because a host that cannot be reached is broken rather than full, `EmptyAnswerLeftOpen`, which took the work and parked the reserve, and an overload from a host that carried nothing else of this gateway's for this model when it refused, counted as it lands rather than when the race reports. `DecodeStalled` narrows severely and leaves the cut-off alone — a slow answer is still an answer, and a host that stalls chronically is withheld by the outlier detector in [`perf`](../perf/) instead.
- **An overload is judged by what else the host was carrying when it refused, not when the race reports.** Verdicts reach `OnResult` only once the whole race is over, by which time the host's other requests may have ended or new ones landed, so `OnResult` leaves the count alone for an overload and the race counts it live instead: the coordinator calls `CountRefusalIfIdle` as it handles the attempt's `AttemptDone`, and the attempt gives its own lease back before it emits that event (`engine/report.go`, `raceCoordinator.countRefusal`; `engine/attempt.go`, `runAttempt`). A host with nothing else of ours in flight for this model at that moment is refusing work rather than full of ours, and the refusal counts towards the cut-off; one still carrying other requests of ours for this model is busy, and the count stays where it was rather than being cleared, which would let a refusing host reset its own streak of faults. The window narrows softly on the race's report either way (`reaction.go`, `ParticipantLimiter.CountRefusalIfIdle`).
- **Lateness is judged while the attempt still runs**, so it leaves the cut-off's count alone: the same attempt is judged again on its own verdict when it ends, and the answer it eventually sends is a `LateSuccess` that clears the count and lifts a half-open probe without widening anything.

## Additive increase

- **Growth is judged on peak in-flight since the last adjustment, not the live count.** The engine releases an attempt's lease before it announces the attempt is done and reports its verdict afterwards, so a live read would see the tokens already given back and refuse to grow a window that was genuinely saturated. The peak is set when the tokens are taken and nothing can undo it, which makes the decision independent of which of the two runs first.
- **Each window is judged and credited on its own.** A window widens only when its own peak reached half its own size, by the tokens its own dimension carried (`window.go`, `window.growIfEarned`).
- **A window grows by one step per answer that used it** — one request of the model's worth of tokens, whatever the answer carried. A wide window earns the same rung as a narrow one and so takes proportionally longer to widen by half (`congestion.go`, `grownBy`).
- **Nothing caps a window.** Where it stops is what the host's congestion signals say; the configuration names a floor and a starting size and no ceiling.
- **A narrowing stops at `Min`, and never below one token.** A host a run of bad answers narrowed still takes one request of that model, and a half-open cut-off still admits exactly one probe.
- **A narrowing restarts the peak of the window it applies to**, and only that one: the growth that follows has to be earned against the narrower window rather than credited to the width the host has just lost (`window.go`, `window.narrow`).

## The cut-off, the peek and the sweep

- **A half-open probe gets exactly one try.** Any fault while half-open reopens the cutoff immediately rather than after `AfterFailures` more.
- **A fault behind an open cut-off counts for nothing.** Attempts that were in flight when it opened keep failing after it; counting them would reopen and lengthen it every `AfterFailures` faults, walking the backoff to `MaxOpen` within seconds of one short outage. Only the probe that follows the cut-off can lengthen the next one.
- **A successful probe clears the trip itself**, not just the half-open flag, or the next `Acquire` would re-flag half-open forever.
- **Backoff is `base * 1.6^count` plus up to 20% jitter** (gRPC connection-backoff's `JITTER`), so reopened cutoffs across many hosts do not retry in lockstep. The count stops rising once the backoff saturates at `MaxOpen`, so `1.6^count` cannot overflow the duration.
- **`Admits` peeks the admit decision** for the smallest possible request, without touching in-flight, the cutoff, or creating state for a participant never seen before, which is what lets routing ask about a host it has never dispatched to. A participant and model with no state answer `open` at once, since a fresh state has no cut-off and nothing in flight; `TestAdmitsAnswersAnUntrackedHostLikeAFreshState` fails if `freshState` ever starts a host anywhere else. The limiter's lock is a `sync.RWMutex` and `Admits` is its only reader: routing asks once per candidate escrow's hosts on every pick, so peeks share the lock with each other and wait only for the writers (`Acquire`, a release, `OnResult`, `ClearQuarantine` and the observers). Anything `Admits` calls must stay read-only. It names which of the two refused — a full window or the cut-off — because the scheduler may cross the first and never the second. `Available` is the same peek as a yes or no.
- **`Overdraft` takes the tokens without asking whether they fit**, and is refused only by the cut-off. It is how the scheduler ends a run of burns on a host that is busy rather than broken; see [routing.md](../docs/routing.md), "The forced send".
- **`Snapshot` copies every pair under one lock acquisition**, so a report cannot mix two moments, then sorts into participant/model order after the lock releases.
- **A pair idle past `IdleEviction` is forgotten.** `Acquire` marks the pair it is asked about as used, and so does the lease it handed out, so a host waiting on a verdict is not swept between the release and the result. The scan runs at most once per tenth of the window and drops only a pair with nothing in flight and no cut-off still running, so a lease released afterwards never lands on a state that is gone. The composition root sets the window to `perf_host_staleness_seconds` through `ParticipantConfigFromConfig`; an `IdleEviction` of zero keeps every pair.

## When a host stops taking work

A cut-off is a decision an operator has to be able to explain afterwards, and its gauge cannot carry it: the first cut-off lasts 5 seconds against a gauge sampled every 15 or 30. `OnResult` and `CountRefusalIfIdle` therefore narrate each edge to the journal bound by `SetNarrator`, under the limiter's lock, and nothing in between, naming which of the two triggers fired — a run of faults, or a half-open probe that failed on its single try — the backoff depth that set the duration, and how long the cut-off will last. The run is counted in `consecutiveCutoffFaults`, over the transport faults, the nonce-abandoning empty answers and the overloads from a host with nothing else of ours in flight for this model alike, while the reason on the line stays `consecutive_transport_faults`: the string is what dashboards and log queries match on, so it is a wire contract rather than a description of the counter behind it (`vocabulary.go`). The volume follows the host count and the backoff, never the request rate. The package imports no logger; the journal writes the line.

## The capacity model

| Quantity | Definition |
| --- | --- |
| `weightConcurrencyLimit` | `floor(weight * per10000 / 10000)`, and 0 when either input is non-positive or not finite |
| `scaleFactor` | `clamp(currentAvailable / full, 0, 1)`, and 1.0 when the baseline is non-positive |
| `escrowWeight` | `Σ currentWeight[host] * hostShare[host]` over available hosts |
| `availableShare` | the same sum with every weight taken as one |

`hostShares[host]` is `slots(host, escrow) / totalSlots(host, model)` across every escrow of the same model that host serves: a host's weight is earned per model, so its escrows of another model take nothing from it. Raw slot counts would count a participant once per escrow it serves instead of splitting it between them.

`Capacity.mu` guards only the snapshot field and the outer membership map — a published snapshot's maps are never written again and each escrow's membership map is the capacity model's own copy — so `ModelWeights` and `EscrowWeight` take the map references under the lock and sum them once it releases.

Three fallbacks decide what a missing view means:

- **A model absent from a *populated* by-model view is served by nobody**, so it scores zero rather than inheriting the generic all-model view. With no by-model view at all, the generic view applies to everything.
- **An empty weight view means the chain named nobody**, not that everybody weighs nothing — a host the chain has reported is a key in the view whatever its weight. When neither view has been observed, escrow scoring falls back to the membership share alone, which serves requests correctly and silently; the `WeightsUnobserved` flag of `ModelWeights` is what makes that state visible.
- **The current and full views fall back to the generic one independently**, so a missing full-by-model entry does not suppress a present current-by-model one.

`ModelWeights` takes the **effective** blocking state, never the chain's raw one. Relaxed mode is the operator's override of that fact, so a capacity that read the snapshot itself would zero the scale exactly when the override was meant to keep serving — and a zero scale clamps every weight-derived cap to nothing. The composition root passes `config.Modes.BlocksRequests` from [`app/capacity.go`](../app/capacity.go), which owns the fold.

## Read next

- [`docs/capacity.md`](../docs/capacity.md) — the ladder, the thresholds, and how the three interact.
