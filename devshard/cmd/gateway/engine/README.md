# `engine` — the speculative race

One client request, several attempts on different hosts, one winner. This package owns that race from the first pick to the vote that settles the last losing nonce.

## What it owns

| File | What it holds |
| --- | --- |
| `race.go` | the coordinator: the event loop and the exits |
| `engine.go` | the engine itself: what it is given, the races it admits, and the barrier it stops behind |
| `report.go` | what each attempt is recorded as, and the one outcome the race reports |
| `pick.go` | asking the scheduler for a host and launching an attempt on it |
| `attempt.go`, `attempt_outcome.go` | one attempt's life: dispatch, receipt, chunks, terminal |
| `escalation.go` | when to start another attempt — the deadline ladder measured from the host's own history |
| `crown.go`, `crown_strikes.go`, `stream.go` | crowning the first attempt to produce content, withholding the crown from a host that keeps answering without, and forwarding only the winner's bytes |
| `deadline.go`, `drain.go` | the timers, and the barrier that outlives the client |
| `classify.go`, `outcome.go`, `terminal.go`, `judgement.go`, `failure.go` | what the attempt ended as, in the vocabulary the ledger admits, and what the race owes the client and the ladders because of it |
| `chunk_head.go` | what a contentless chunk carried, with the ids and logprobs the gateway strips elided |
| `settle.go`, `session.go`, `settle_queue.go` | the timeout vote every unfinished nonce owes, where it waits for its deadline, and how many post at once |
| `reassembly.go`, `carry.go` | rebuilding events split across chunk boundaries |
| `vocabulary.go` | the wire strings — metric labels, log fields, ledger reasons — declared once |
| `trace.go` | the `RaceStep` a coordinator copies at emit, for the journal |
| `winner_sample.go` | the served winners whose sample and metric labels wait for their nonce's finish |
| `errors.go`, `capability.go` | what the race hands back, and what a host's refusal says about the host |

## What it does not own

It does not choose the escrow or commit the nonce — that is [`scheduler`](../scheduler/). It does not shape the request or the reply — that is [`filters`](../filters/). It does not write the ledger — it reports one outcome, and [`nonces`](../nonces/) records it. It does not write its trace lines either: it copies each step into a `RaceStep` and hands it to `Deps.Journal`, and [`journal`](../journal/) renders it.

## Boundaries

- **A losing attempt is not a free attempt.** Its nonce is committed and costs the escrow; the race is not over until every one of them has been settled or reported.
- **The race outlives the client.** A client that hangs up does not cancel the attempts, because their nonces still owe votes. The drain barrier is what makes shutdown wait for them.
- **The outcome is reported exactly once**, from whichever goroutine ends the race.
- **An SSE error event counts as a chunk but never crowns.** A host that answers with an error has answered something, but not content.
- **A step is copied on the coordinator goroutine.** `RaceStep` holds values, and its `Outcome` is a copy of the attempt's, so nothing the coordinator owns crosses to the journal's goroutine. The one step the engine emits itself, a held winner's finish landing (`winner_sample.go`), is built from the copy the hold took. `Deps.Journal` is optional and guarded; a race without one writes no trace.

## From pick to report

`begin` picks under the client's context bounded by `schedulerPickTimeout`: the bound exists because a scheduler waiting for capacity would otherwise hang, and the client's context exists so a client that leaves while its pick waits gets no primary. A pick that fails once the client has left still records the departure in `Lifecycle.ClientGone`, as `detach` does. It resolves the escrow's `DispatchTarget`, reads the phase snapshot for the PoC bypass and the attempt budget, asks the policy for a `StartPlan`, launches the primary, and starts however many more immediate attempts the plan asked for.

`await` then loops: settle held crown claims, compute the next deadline, and block on attempt events, a finished pick, a crown claim, the timer, or the client leaving. It exits when nothing is pending, no pick is running, and no escalation is armed — an armed escalation outlives the last pending attempt, because a race whose every attempt failed is owed another.

There are three exits. `exitComplete` ends the race. `exitWinnerServed` and `exitClientGone` hand the still-pending losers to a second `await` on another goroutine, which reports; the outcome the caller gets back is the client's view, and the reported one may be finished later.

A nonce the race can no longer spend is **stranded**, not dropped: it is committed, paid for and answered by nobody, so it enters the attempt list with `TerminalNoReceipt` and is carried into the timeout plan.

## Who owns which state

- `liveAttempt` is written only by the coordinator, from `AttemptEvent`s and from its own deadline firings.
- `attemptState` is owned by the attempt's own goroutine and never read by another; every fact the race needs travels as an `AttemptEvent` instead. `AttemptSpec` is fixed at construction and never written afterwards.
- The event channel is buffered (`eventBuffer`) so chunk progress does not park a host's goroutine. `emit` blocks — terminal events must arrive — while `offer` drops progress the coordinator is too busy to take. `expire` drains the queue before reading `lastChunk`, so a dropped progress event only ever ages it between reads. Every select arm that reads race state rather than adding to it starts with `catchUp` for the same reason.
- Events must be drained until every started attempt has delivered its `AttemptDone`. See rules.md, "1. A committed nonce is always settled".

## Escalation and the deadline ladder

`EscalationPolicy` is pure over its arguments: no host performance, no phase snapshot, no clock of its own. `nextDeadline` takes the earliest armed deadline, with the declaration order of `deadlineTrigger` breaking exact ties.

An `ArmedEscalation` is a deadline to arm, not a permission to escalate; only `Confirm` converts it, by re-deriving the same stage at the same deadline. The attempt's `escalated` flag is consumed before the pick starts, so a pick that finds no host cannot retry the same trigger.

Escalation is disarmed entirely when a pick is already running (that pick *is* the escalation), when the client has left (another attempt is another nonce to settle for a response nobody will read), when an attempt has been crowned, when the budget is spent — as many unfinished attempts held as it allows, or as many started as the host group has — or when a trusted host's refusal or rejection of the request rules out a retry (race.md, "Escalation").

The rungs, in `triggerFor` order: an already-escalated attempt never arms; a suspicious host arms immediately; a done attempt whose nonce is finished never arms unless it answered with an empty stream, and any other done attempt arms immediately; an attempt with no receipt arms at `SendTime + receiptTimeout`; an attempt with a first token never arms; everything else arms on the first-token curve. With `HedgeFirstTokenFloor` above zero, `hedgeBefore` moves the last two to the slow-start hedge whenever it falls before them; the hedge waits the larger of the floor and the curve times `EscalationAttempt.FleetHedgeScale`, which each attempt reads from the model's fleet tracker at launch, or the floor and the curve while that is zero (race.md, "Escalation"). The tracker lives in the engine rather than beside the per-host windows in `perf`: it needs the first-token curve and the age of first attempts still in flight, which only the race knows. It is optional in `raceDeps`; a race without one hedges on the floor and the curve.

`owedDeadline` gives the coordinator the same receipt and first-token deadlines without escalation's conditions: an attempt that already escalated, a spent budget or a crowned rival do not excuse a host from being late. `judgeMissedDeadlines` narrows each late host once per deadline and leaves the attempt running, under `MissedReceiptDeadline` or `MissedFirstTokenDeadline` — severe on both congestion windows for a receipt that never arrived, severe on the input window and cross on the output one for a first token that did not (`limits/README.md`, "What blames which window"); during proof-of-compute generation it records the deadline as excused and moves no window. `triggerFor` and `owedDeadline` both read `receiptDeadline` and `firstTokenDeadline`, so the escalation and the judgement cannot disagree about when a host is late; the slow-start hedge may start a rescue earlier, but it judges no host.

- **Receipt timeout doubles above `receiptTimeoutDoubleAboveTokens`** — admitting a very large prompt is itself work.
- **The first-token curve** is the measured fit over prompt size: a fixed cost to start answering (`firstTokenBaseSeconds`), a per-token cost to read the prompt, and a quadratic term that only matters on very large prompts. It is floored at `FirstTokenFloor` and capped at `FirstTokenCeiling`, because uncapped the quadratic outgrows the backstop that cancels the attempt; a zero ceiling means the operator removed the cap.
- **A host's own history only ever extends the budget.** `firstTokenBudget` uses the observed first-content p75 × `firstTokenObservedSlackNumerator / firstTokenObservedSlackDenominator` (1.5×) only while it is within `firstTokenObservedLimit` × the curve and longer than the curve; a host slower than that keeps the curve, because waiting out its own history would delay the attempt that rescues the request. The quantile is read once per attempt, since `plan()` runs on every event and it cannot move mid-attempt.
- **The curve is measured from dispatch**, but a receipt that used more than the curve allows would leave the rung already due, so the judged deadline is never earlier than `ReceiptTime + FirstTokenFloor`: the host owes a first token, not the time its receipt took. The hedge is exempt from that floor and can still fire earlier.

### Backstops are not tunable

`streamingHardTimeout` and `schedulerPickTimeout` bound a request every tunable already failed to bound. The streaming backstop is 20 minutes, but gives way if the chain's own execution deadline ever moves below it — a stream held past that deadline is work nobody can be paid for.

Not tunable still means not tunable **by an operator**. `Deps.E2E` can shorten the streaming backstop, and nothing but a declared end-to-end stand can fill it: see [`docs/rules.md`](../docs/rules.md), "What a test stand may reach".

## Crowning and the client's bytes

`winnerWriter` is one attempt's claim on the client stream. `client` is the only path to the client anywhere in the writer, and `withheld` is the only source it is ever assigned from, so a losing attempt has no reachable sink at all rather than a sink guarded by a branch somebody must remember to write.

- `Write` buffers every pre-content chunk and claims the crown on the first content chunk; a suppressed attempt reports success without writing. The buffered prefix is charged to the engine's prefix budget, and an attempt past it is cancelled with the budget's cause (`prefix_budget.go`).
- `claim` blocks on the coordinator's single answer, and abandons if the race ends first. `Abandon` clears the verdict, the prefix and both writer fields: an attempt that ends without earning the crown must never have what it buffered forwarded.
- `Flush` needs no gate of its own — an uncrowned attempt has no client to reach. `attemptWriter.Flush` is how the transport's per-line flush reaches the client at all: without it the assertion the transport makes on its writer fails and a crowned winner's bytes sit in the server's buffer.
- `contentGate` hands the sink beside it the one fact an `io.Writer` signature cannot carry. `Classify` is called immediately before the `Write` of the same chunk, on the same goroutine.

The coordinator answers claims in `answer`: an unknown nonce or an already-crowned race is suppressed, a **suspicious host's claim is held rather than refused**, because a refusal is permanent, and anything else is crowned on the spot. `settleClaims` answers the held claims once the race can tell whether a rival will serve — a rival being a pending attempt beyond the claimants, a running pick, or an immediate attempt still owed. Alone, a suspicious host is crowned, and so it is once every running rival has missed its receipt or first-token deadline with no token to show (`rivalsMissedTheirDeadline`). `crownWinner` is the single place one attempt becomes the client's answer, so the reason travels with it into the trace.

### Crown denial

`crownStrikes` withholds the crown from a host that answers without content, while leaving it in the scheduler's rotation. `crownDenialStrikes` content-free answers deny it; a content-bearing answer removes the entry, and entries are otherwise never evicted (see rules.md, "9. Bounded by construction"). `suspicionGate` folds the operator's manual never-trust-this-host pins into the same gate.

Only attempts that say something about the host are observed: it answered with content, or it claimed to serve and produced none. A dial failure, a stranded nonce or a cancelled client says neither, and clearing the host's strikes on one would hand it a clean record it did not earn.

`crownStrikes` narrates the two edges — denied at the third content-free answer, restored by the next answer with content — through `Deps.Journal`, whose `raceJournal` interface includes `crownNarrator`. `Observe` calls `HostDeniedCrown` and `HostCrownedAgain` while it holds `crownStrikes.mu`, so the narrator must queue and return: the journal only appends the event, and its own lock is a leaf that takes no other lock ([`journal/README.md`](../journal/README.md), "Order and the locks it takes"). The journal writes `host denied the crown` and `host crowned again`; a nil journal leaves the strikes working and silent.

## Classification and reassembly

`carryBudget` bounds the bytes held for SSE reassembly at three levels — attempt, participant and global. `reserve` charges the participant first and then the global pool, undoing the participant charge when the global pool trips. A participant's counter is created on first use and never removed (rules.md, "9. Bounded by construction").

`carryBuffer` reassembles events across chunk boundaries for one attempt and belongs to that attempt's goroutine alone; only the byte charge it holds is shared. `Take` returns every newline-terminated event in the held fragment plus the chunk and retains the rest; a cap trip releases the fragment and yields the raw chunk instead, so classification degrades to whatever parses on its own rather than stopping, and the first such trip is reported once as a classify overflow. `Tail` is the unterminated final fragment: a stream whose last event arrives without a newline is classified from it by `Flush`, or it reads as having produced nothing.

What the classifier reads out of an event:

- **Content** names the field carrying the first client-renderable output. `choices[].text` is excluded: the gateway serves only `/v1/chat/completions`, where a host emitting it renders nothing. A `stop` with host-reported completion tokens and no output counts as content only on an empty-answer route, whose profile sets `empty_answer_counts_tokens` (the `kimi` preset); it is also the only route whose `completion_tokens` can separate a model that produced nothing from a host that carried nothing.
- **Errors** are extracted in both the nested `{"error":{...}}` shape and the flat `{"object":"error",...}` one vLLM emits, without a byte-wise pre-filter: the host writes these bytes, and a key spelled with a `\u` escape would pass the scan while the decoder reads it as the error it is, leaving the attempt classified as something it is not. The parse a pre-filter saves does not offset that misclassification.
- **Usage** is pre-filtered on the `"usage"` key, which is sound because vLLM emits the usage object once per stream, so the check skips almost every chunk.
- **A capability refusal is kept out of `Error`** and travels as `CapabilityRefused` instead, so it does not end the race; its message still reaches the perf recorder.

## Outcome, samples and verdicts

`Terminal` is an attempt's classified end state, and every downstream vocabulary — limiter verdict, perf sample, metric label — is a total function of it. `terminalStatuses` is the one table linking an upstream status to the terminal it produces: `StatusFor` reads it forward for metrics and dispatch-error classification reads the derived inverse, `terminalForStatus`, so the two cannot disagree about which status a label describes. A host refuses over HTTP or over Connect, and `hostRefusal` reads a Connect refusal as the HTTP status its code stands for (`transport.ConnectApplicationStatus`) before anything classifies it: a host that rate-limits a chat with `resource_exhausted` is throttling, and backs off as a `429` would, rather than counting as a dead transport toward its cut-off. `Terminal.String` falls back to `reason()` rather than keeping a second list, so the failure names cannot drift apart; `reason()` is empty for the two outcomes that are not failures, which `failureReason` relies on to fall through.

`racedTerminal` is applied by the coordinator because an attempt goroutine knows only its own cancellation, not the crown, the silence, the backstop or the nonce's finish: a cancelled attempt becomes a hard timeout when the backstop fired on it and stalled when it went silent mid-content or when the hosts abandoned the race; an attempt that ended as an answer becomes stream-truncated when its host closed the stream with neither `[DONE]` nor a finished nonce, crowned or not, and otherwise won when it is the crowned attempt. The transport sets the same terminal itself for a broken transfer (`transport.ErrSSEStreamTruncated`), but a `devshard_receipt` counts as its terminator, so an answer with a receipt and no `[DONE]` reaches the coordinator as a clean one and is checked here. A truncated answer is not won or lost for routing or perf either: outside the long-response exemption its verdict is the transport fault a broken transfer earns, counting towards the host's cut-off, and its sample is unresponsive, as for any unfinished nonce. That holds for a loser too: a loser whose transfer broke already counts against its host, so one whose host closed the stream cleanly on an unfinished nonce counts the same. An attempt the race stopped listening to still gets an `unreportedOutcome`, otherwise its spent nonce leaves nothing downstream can see. `phaseAborted` is asked with the phase the coordinator sees, so the log line and the ladders cannot disagree; a no-receipt attempt never reached the host's queue, so a phase transition cannot be what ended it.

### The exemption ladder

`sampleExemption` is the ordered set of reasons an attempt contributes no perf sample and therefore never counts toward its host's ejection: never dispatched, never reported (the race stopped listening, so judging it would charge a host for the gateway's own cancellation), phase aborted, error stream or capability refusal, state divergent, the gateway's own unavailable root or a slot its session holds no client for (`user.ErrHostAbsentFromChain`, which also reports `HostAbsent` to the picker), the gateway's own prefix pressure, long response, empty stream under a PoC bypass, a request the host refused as too large, empty stream with no winner, and finally client-cancelled, unless the cancelled attempt lost a served race after missing an unexcused deadline without a token.

`Verdict` is a second ladder and **disagrees with the sample ladder at client cancellation** — the race cancels its own losers. An empty stream is an `EmptyAnswer`, or an `EmptyAnswerLeftOpen` when it left its nonce open, and a stream that went silent between chunks is a `DecodeStalled`. The ladder also turns a burn-empty held past `emptyStreamHeldTooLong` into an overload verdict: below that a burn-empty is the model's output, at or above it the host held the request past the refusal point and returned nothing. A won or lost attempt whose host missed a deadline is a `LateSuccess`: its windows were already narrowed at the deadline, and a success would widen them again.

`responsive` decides whether a host earns a positive sample: confirmed, nonce finished, and not an empty stream. A long non-stream reply is exempt from being judged slow, but an empty one earns nothing — crediting a host that held the request for the whole window and returned no content teaches the router to prefer it. The long-response exemption gates on `ContentSource`, not `ContentChunks`, which counts error events too (rules.md, "1. A committed nonce is always settled").

A won attempt whose nonce is still open when it completes is not judged on that read, because its host's Finish usually lands after the stream ends. `Engine.record` holds its sample and its metric labels (`FinishAwaited`, `heldWinners`) and decides them once: finished when `Engine.OnFinishApplied` reports the Finish applied, or at the post of the winner's execution vote, finished if the nonce reads finished by then and still open otherwise (race.md, "A served winner's sample waits for its finish").

### Measurements

- `IsWinner` is the one test for "this attempt won the race". Whether a client was still there to receive it is `Lifecycle.ClientGone`, which visibility reads: the race outlives the client.
- `ClockOffset` compares the executor's stamp against the midpoint of the send-to-receipt window rather than the dispatch, which would charge the host for the outbound leg; half a second is added back because the executor stamps whole seconds downward.
- `TimePerOutputToken` starts at the first content chunk, so prefill is not charged to decode speed.
- `MaxChunkGap` keeps the longest silence a host left mid-stream, which a chunk count cannot show. The silence before `[DONE]` is the end of the stream, not a host that went quiet. `MeanChunkGap` is the inverse of the delivered rate.
- A served attempt's label reason is blanked, except under `VisibilityNoWinner`: blanking it there renders the panel's headline case as "unknown".

## Timeout votes

Every nonce the race did not leave settled owes a chain vote. `TimeoutStep.StartedAt` is the race's start, not the attempt's dispatch: verifiers recompute a refusal deadline from the committed record, so every nonce a request commits must carry the one stamp, dispatched or stranded.

`timeoutSkipReason` names every skip — phase aborted, empty stream with a finished nonce, finished nonce, long response. A host whose escrow state diverged is not one of them. `settleSteps` reports each step through its callback as it happens: a posted vote's started event before the post and its result once the post returns, because a vote round runs for minutes and a started event held until the end hides a vote in flight from the ledger and the metrics. A step nobody will attempt is reported only as skipped, because a started event with no completion following reads as a hung settle. `settleSteps` also returns the steps a refused post is worth retrying, so the caller can queue another round instead of treating the failure as final (race.md, "The swept vote and the retried vote").

`Deps.Timeouts` is resolved per race rather than held, because escrows rotate, and it is handed the request params because the vote must carry the prompt the committed record keeps only as a hash. Every `TimeoutEvent` carries the race's `RequestID`, and the settle goroutine posts under `settleContext`, which puts that id on the context so the shared session's `timeout_*` stage lines carry `request`. An empty id adds nothing, because `logging.WithRequestID` would mint one.

`SettleTimeout` reads the handler's own record of whether the vote reached the escrow state: the handler returns a non-nil error on its success path too — that error carries "the inference timed out" to the request — so the error alone cannot tell a settled vote from an unsettled one. `TimeoutOutcome` prefers the handler's own detail over the generic collection error, because that is the only place the refusing verifier is named; `escrowMissing` is the caller's reading, since vote collection reports a count and never the verifier's error.

Every event `settleSteps` reports goes to `Deps.Metrics`, and nowhere else. The composition root forwards it to the [`journal`](../journal/), which writes `timeout vote failed` for a vote that never reached the chain.

`settleQueue` holds what a race owes on a runtime timer instead of a goroutine. A race's votes go in as one task per distinct vote deadline (`voteGroups`), each armed at the earliest deadline among the votes of its group it will actually post (`earliestDue`), and `time.AfterFunc` spends a goroutine only once that moment arrives. `TimeoutPoster.VoteDeadline` is what makes this possible: the deadline is read before the wait rather than inside `HandleTimeout`, so a vote due in an hour costs one timer until its hour is up.

`Engine.settle` therefore returns as soon as its tasks are held, one per distinct vote deadline, never when a vote is posted, and the race's registration is released by the last of them — so the escrow and the `Stop` barrier are held exactly as long as the vote is owed, as before. The configured limit is read on each `Add` and applies to posting only: a due vote takes a place if one is free and otherwise queues for the next place a finishing vote gives back, and no vote is dropped (race.md, "The timeout-vote queue").

## The Stop barrier and the escrow hold

`Engine.Stop` refuses new races and then waits: it returns once every race it admitted has posted the vote that settles its nonces. A failed refused vote is retried up to four times on the vote queue before the race lets go of its registration; `Stop` posts a retry still waiting on its timer at once and schedules no further round (race.md, "The swept vote and the retried vote"). `admit` registers a race under the lock before it starts, and returns nil once stopped. A panicking race still owes the barrier its release, and the panic itself is re-raised unchanged.

`raceRegistration` is that place in the barrier and the holder of the escrow's in-flight count. Releasing is idempotent because the settling path and a panicking request path both reach for it and only one may be counted. It keeps the *first* hold for as long as the race's vote is owed — every hold a race is offered counts the same escrow, so a later one goes straight back rather than displacing the one being kept — and `launch` gives the commit's own hold back immediately for that reason. It keeps the race's dispatch target beside the hold, so a held sample can read its nonce's finish when the winner's vote is posted.

Facts the race observes about the escrow but must not act on travel in `Lifecycle` and are handed to the lifecycle observer on the response path, which must mark and return rather than reach the chain. The escrow-missing mark is made before the ledger row is queued, so an observer that has seen the row has also seen the mark.

## Errors the engine returns

- `ErrStopped` — the engine can no longer see a request through to a settled nonce.
- `ErrWinnerIncomplete` — the crowned attempt's bytes are already on the wire, so no other attempt's payload can be put in their place.
- `ErrAnswerTruncated` — the crowned attempt streamed content and ended `TerminalStreamTruncated` with neither `[DONE]` nor a finished nonce, whether the stream closed cleanly or broke; a broken transfer whose host sent `[DONE]` or finished its nonce is `ErrWinnerIncomplete`. The api writes it without a `[DONE]` (see `api/README.md`, "Streaming the reply"). Its nonce goes through the timeout plan like any unfinished one.
- `ErrEmptyStream` — every attempt ended empty.
- `ErrHostsUnavailable` — every attempt ended `TerminalUnavailable` or `TerminalThrottled`, and no host error ruled the answer: the shard had nothing wrong with the request, only nowhere to send it.
- `ErrPoCTookTheHosts` — every attempt was ended by proof of compute starting on its host (`PhaseTransitionAborted`): it wraps `ErrHostsUnavailable`, so the client gets the same retryable 503 instead of the `ErrEmptyStream` 502 the empty streams would otherwise earn, and the log still names the cause.
- `ErrAllAttemptsFailed` — nothing above applied.
- `HostApplicationError` — an upstream refusal that is the response: the host answered, and the API writes its message, or its payload when it has no message, under the status `HTTPStatus` reads from its code or class, 502 when neither names one (`api/errors.go`, `writeErrorFor`). `hostError` prefers the crowned attempt's refusal, because a host chosen to answer is the one whose answer the client asked for, and after it a trusted host's refusal or rejection of the request that rules out a retry (race.md, "Escalation").
- `StatusForError` maps a host application error to its own status, `ErrHostsUnavailable` to 503, and an upstream throttle or unavailability to 429.

Two sentinels classify one attempt rather than the request (`attempt.go`):

- `ErrStateRootDivergence` — the host's post state root diverges from the gateway's; the race branches on it without reading error text.
- `ErrLocalRootUnavailable` — the gateway's own store failed to give a post state root, where the host did nothing wrong.

## Read next

- [`docs/race.md`](../docs/race.md) — the ladder, the crown, the drain, and why each is shaped that way.
