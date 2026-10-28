# `journal` — one ordered path to a log line or a ledger fact

Race outcomes and race trace steps, request records, limiter refusals and uncached replies, burns and burn-budget trips, timeout votes, composed diffs' ledger facts, warmup probes, host transitions, escrow transitions, chain transitions, and nonces served on a host their request excluded all pass through here, in the order they happened; this package decides which of those become log lines and which the nonce ledger applies.

## What it owns

- **`Journal`** — built once in `app/compose.go`, closed by the `journal` shutdown step. Producers call its methods; one consumer goroutine hands each accepted event to the sinks.
- **Two consumer-side sinks.** `logSink` defaults to `devshard/logging`, so `internal/logcapture` sees every line. `ledgerSink` is `*nonces.Recorder`, or nil when nonce accounting is off.
- **Its counters**, returned by `Counts` and exported by `metrics.NewJournalCollector`.

## What it does not own

- **Nothing is persisted.** An event lives in memory between the producer's call and its delivery. At two to three million nonces a day an event row per step would cost about 10 GB a day, so the ledger keeps its per-escrow counters and the logs keep their handler.
- **Metrics stay synchronous.** A recorder in `metrics` is still called by the producer; only lines and ledger facts are queued.
- **It does not classify.** Which counter a nonce lands in is `accounting`'s decision.
- **The sweep writes the ledger directly.** `Recorder.sweep` (`nonces/sweep.go`) calls `Book.OpenEscrow`, the `Observe*` methods and `RetireEscrow` on its own goroutine, never through this package.
- **The warmup's `OpenEscrow` is a direct write too.** `Prober.openLedger` (`warmup/warmup.go`) calls it itself, because the open must land before the probe reaches the ledger through the journal.
- **Process, admin and store lines are written where they happen;** "Who may log directly" names the files.

## Boundaries

- **Producers never import this package.** `engine`, `scheduler`, `perf`, `limits`, `registry`, `escrow`, `chain`, `nonces` and `warmup` declare the interface they call; `api` imports it only for `RequestLine`. The composition root adapts the rest (`app/observers.go`).
- **`DiffFact` is declared in `accounting`** and aliased here, so `*nonces.Recorder` satisfies `ledgerSink` without importing this package.
- **A disabled ledger is a nil interface, never a typed nil.** `journalSettings` in `app/observers.go` leaves `Settings.Ledger` unset when the recorder is nil; a nil pointer inside the interface is non-nil to the consumer's check.
- **No sink writes what it is handed.** A race outcome's attempts are the slice `api` reads on the response path.
- **A renderer allocates the fields of every line.** A logger may keep the slice it is given; `logcapture.Recorder` does.
- **A race step is already a copy.** The coordinator fills `engine.RaceStep` on its own goroutine, the terminal it would log already raced and the phase mark already computed, so rendering reads no coordinator state. `RecordStep` queues a pointer to its own copy of the step, so the journal's mutex never copies an attempt outcome.
- **`api` reads the client stream before it hands the line over.** `RequestLine.Bytes` and `Terminated` are taken on the handler goroutine, because attempt goroutines can still write the stream after the handler returns.

## Two lanes

| Lane | Kinds | Refused when | Counted in |
| --- | --- | --- | --- |
| money | `KindRaceReported`, `KindTimeoutVote`, `KindNonceBurned`, `KindBurnBudgetExhausted`, `KindDiffComposed`, `KindWarmupProbe`, `KindNonceStranded`, `KindHostDiverged`, `KindReplyNotCached`, `KindRequestFinished`, `KindHostTransition`, `KindExcludedHostServed`, `KindForcedSend`, `KindEscrowTransition`, `KindChainTransition`, `KindFundingTransition` | `MoneyCeiling` (100 000) money events are accepted and not yet delivered | `devshard_gateway_journal_money_refused_total` |
| progress | every other kind: a dropped line costs a log line, never a ledger fact | `ProgressBacklog` (8 192) progress events are accepted and not yet delivered | `devshard_gateway_journal_progress_dropped_total` |

Both lanes share one queue, so a line and a ledger fact keep the order they happened in. A lane releases an event when the consumer has delivered it, not when the consumer takes it out of the queue, so the batch being delivered still counts against its lane. A producer never waits: admission is an append under the journal's mutex, and a refusal is a counter. The queued value stays a few words wide: every payload wider than a few words is copied once by the producer method that receives it, outside the mutex, and queued as a pointer, so neither the append nor a growing queue copies a payload under the mutex. The money ceiling is not back-pressure; it bounds memory when the consumer is stuck, and a refused money-lane event is a ledger fact never applied or a money-lane line never written (a stranded nonce, a divergent host, an uncached reply, a finished request's own record, or a host, excluded-host, chain or escrow transition), which is why `Close` returns an error when any were refused. After the batch that follows a progress drop, the consumer writes one Warn line, `journal skipped progress lines`, with `skipped_lines`. That batch may be empty: the consumer does not wait while a drop is still unwritten, so the line reaches the log before `Flush` or `Close` returns. A drop never needs to wake the consumer, because a lane is full only while an event it counts is queued or being delivered, and the consumer sleeps only when neither holds.

## Order and the locks it takes

- **The consumer never holds the journal's mutex while it calls a sink.** It swaps the pending slice out under the mutex and delivers the batch outside it.
- **A burst's buffer is not kept.** The consumer reuses a delivered batch's slice for the next swap only while its capacity is at most `retainedBatchCapacity` (4 096), so one burst's buffer is not held for the life of the process.
- **The mutex guards a slice of small values.** A producer's payload is copied once, outside the mutex, when its method receives it; under the mutex only a pointer and a few scalars are appended, however long the queue grows.
- **Five producers call in under their own lock.** The session diff observer under the session lock (`devshard/user/session.go`, `SetDiffObserver`); `perf.Tracker` under `t.mu`; `limits.ParticipantLimiter` under `l.mu`; the crown strikes under `crownStrikes.mu` (`engine/crown_strikes.go`); and `registry.Registry`'s `Add` and unpublish under `r.mu`. Each call ends in `emit`, which holds `j.mu` only to admit the event and takes no other lock under it, so `j.mu` is a leaf and no cycle can form.
- **The diff observer allocates once.** `DiffComposed` counts the diff's ledger facts before it allocates, copies them into `DiffFact` values and appends one small event; the book's own lock is taken later, on the consumer, never under the session's. The observer's copy of the diff is its one allocation; `DiffComposed` adds none for an inference's diff with no verdict and no applied timeout, and queues nothing; a diff with no inference always queues its service nonce.
- **A slow ledger delays every line.** Lines and ledger facts share one consumer, so while `Book.Snapshot` holds the book's read lock the consumer waits on its next ledger fact, and progress lines past the backlog are dropped.
- **A line's timestamp is when the consumer wrote it**, not when the step happened, and lines of different kinds can interleave differently than when each producer wrote its own.

## Flush and Close

- **`Flush`** returns once every event accepted before the call has reached the sinks and every drop counted before it has been written in a skipped-lines warning. Tests call it before asserting; a sink must never call it.
- **`Close`** refuses later events, drains what was accepted, writes the skipped-lines warning still owed, stops the consumer, and returns an error naming how many money-lane events were refused over the journal's life. A second call returns at once.
- **An event after `Close`** reaches no sink. It is counted in `devshard_gateway_journal_late_events_total`, and the first of each kind is written straight to the log sink as the Error line `journal received an event after it closed` with its `kind`. Only work still running when the journal closes can be late: a timeout vote still posting after the `races` drain ran out of budget, an in-flight warmup probe, a handler still running after the listener's shutdown, or the republish after a devshard write, whose goroutine `serve` awaits only after shutdown (`app/devshards.go`, `republishOnDevshardWrites`). A late service diff is not lost for good: the next run's ledger takes it from the restored session. See [`docs/accounting.md`](../docs/accounting.md), "Storage".

## Shutdown

The `journal` step sits between `escrow sessions` and `nonce accounting` (`app/lifecycle.go`, `shutdownOrder`). Every producer that owns a shutdown step stops above it and the ledger it feeds closes below it; the warmup, which is only cancelled, and the republish after a devshard write can still narrate after the journal closes, and such a line is counted as a late event. It is not `needsQuiesced`: closing it destroys nothing a running step reads, and a late event is counted rather than lost unseen. The step closes through `closeWithin`: it waits for the drain up to the shutdown budget, and at least one second (`journalCloseFloor`) even when a drain step above spent the budget, then reports the step abandoned, so a sink that never returns cannot keep nonce accounting and the store from closing. A close that returns just as the budget runs out reports its own result, so a money-lane refusal is never replaced by the abandonment.

## Kinds

| Kind | Producer | Ledger | Line |
| --- | --- | --- | --- |
| `KindRaceReported` | `nonceAccountedRaces.RecordRace` (`app/observers.go`) | `RecordRace` | none |
| `KindTimeoutVote` | `nonceAccountedRaces.RecordTimeout` for a race vote; `probeVotes.RecordTimeout` through `RecordProbeTimeout` for a warmup vote (`app/observers.go`) | `RecordTimeout` | `timeout vote failed`, Warn (`render_money.go`): a race vote whose action is `failed` for any reason but `escrow_gone_from_hosts`, which the escrow's own line already reports once. A warmup vote writes none; the warmup writes its own line |
| `KindNonceBurned` | `tracedDispatches.GhostBurned` (`app/observers.go`) | `RecordGhost` | `nonce burned for nobody`, Warn (`render_money.go`) |
| `KindBurnBudgetExhausted` | `tracedDispatches.BurnBudgetExhausted` (`app/observers.go`) | none | `escrow stopped burning nonces at its budget`, Warn (`render_money.go`) |
| `KindDiffComposed` | the diff observer `nonces.Recorder.watchDiffs` installs, through `DiffComposed`, under the session lock | `RecordDiffFacts` | none |
| `KindWarmupProbe` | `warmup.Prober.record` through `ProbeRecorded` | `RecordProbe` | `escrow warmup could not settle its nonce`, Warn (`render_money.go`), when `RecordProbe` returns the book's refusal |
| `KindNonceStranded` | `raceCoordinator.strand` through `RecordStep` (`engine/race.go`) | none | `nonce stranded`, Warn (`render_race.go`) |
| `KindHostDiverged` | `raceCoordinator.stateDiverged` through `RecordStep` (`engine/report.go`) | none | `host blocked for state divergence` or `host rewound for state divergence`, Warn (`render_race.go`) |
| `KindNonceCommitted` | `raceCoordinator.launch` through `RecordStep` (`engine/pick.go`) | none | `nonce committed`, Info (`render_race.go`) |
| `KindEscalationUnfilled` | `raceCoordinator.reportUnfilledPick` through `RecordStep` (`engine/pick.go`) | none | `escalation unfilled`, Info (`render_race.go`) |
| `KindAttemptCrowned` | `raceCoordinator.crownWinner` through `RecordStep` (`engine/crown.go`) | none | `attempt crowned`, Info (`render_race.go`) |
| `KindAttemptFinished` | `raceCoordinator.complete` through `RecordStep` (`engine/report.go`) | none | `attempt finished`, or `attempt finished with no outcome` when the attempt reported nothing, Info (`render_race.go`) |
| `KindWinnerFinishLanded` | `Engine.decideWinner` through `RecordStep` (`engine/winner_sample.go`) | none | `served winner finish landed`, Info (`render_race.go`), with `finish_lag_ms` from the attempt's end until the gateway saw the finish |
| `KindReplyNotCached` | `Server.chat` through `ReplyNotCached` (`api/routes.go`) | none | `a host stopped mid-answer: reply served, not cached`, Warn (`render_request.go`) |
| `KindRequestFinished` | `Server.finishRequest` (`api/finish.go`) and the cache hit in `Server.chat` (`api/routes.go`), through `RequestFinished` | none | `request finished` (`render_request.go`): Info when the request went out clean, Warn with a race or delivery error; a cache hit writes the short shape with `outcome` `cache_hit`, its prompt count as `input_tokens` and where that count came from as `input_tokens_source` |
| `KindRequestThrottled` | `Server.chat` through `RequestThrottled` (`api/routes.go`) | none | `gateway limiter turned a request away`, Warn (`render_request.go`) |
| `KindHostTransition` | `perf.Tracker`, `limits.ParticipantLimiter`, `engine` crown strikes | none | host withheld and back, capability refusals, cut off and back, crown denied and restored (`render_host.go`): Info, except `host withheld from routing`, `host cut off after transport faults` and `host denied the crown`, which are Warn |
| `KindExcludedHostServed` | `tracedDispatches.ExcludedHostServed` (`app/observers.go`) | none | `nonce spent on a host the request excluded`, Info (`render_host.go`) |
| `KindForcedSend` | `tracedDispatches.ForcedSend` (`app/observers.go`) | none | `nonce sent over a full congestion window`, Info (`render_host.go`) |
| `KindEscrowTransition` | `registry.Registry`, `publishEscrows` (`app/devshards.go`), `escrow.Manager`, `chain.TxClient`, `warmup.Prober` | none | escrow published, retired, drained, created, settled; `escrow created` carries `reason` (a planner reason, `bridge` for a bridge temp, or `operator` for the admin create); boot and warmup failures (`render_escrow.go`, `render_warmup.go`): Info, except Warn for `settlement signatures did not verify`, `devshard cannot be served, marking inactive`, `devshard could not be opened, retrying`, `commitment cleared`, `escrow gone from chain, taken out of service`, `escrow parked at its settlement deadline`, `settle margin shorter than two settle windows`, `escrow deadlines read past a stale chain height`, `escrow chain epoch and amount unresolved`, `escrow marked for replacement` (a nonce cap only), `rotation skipped, the network serves no such model`, `regular escrows promoted to temp`, `escrow warmup found no nonce to spend`, `escrow warmup could not open the escrow in the ledger` and a failed vote's `escrow warmup voted on its unfinished nonce`, and Error for `escrow deadline passes unsettled`, `draining escrow failed to close, its storage stays held` and `escrow tick failed` (a busy or in-flight settle is deferred and is not part of it) |
| `KindChainTransition` | the chain observer's health edge through `healthNarrator`; `phaseNarrator` (`app/observers.go`) for epoch and blocking changes | none | chain epoch, blocked and unblocked requests, snapshot stale and recovered (`render_chain.go`): Info, except `chain blocked requests` and `chain snapshot stale`, which are Warn |
| `KindFundingTransition` | the escrow manager's funding planner tick (`escrow/planner.go`), through `EscrowPlanned`, `EscrowStarved`, `EscrowFull`, `FundingGuaranteeBroken`, `FundingMisconfigured`, `EscrowBudgetReached` and `FundingPlanFailed`; on the money lane, so a once-per-episode line never drops; `escrow starved` and `escrow full again` after the first sighting only once the new state held for two planning reads; a broken guarantee is one episode until the guard shortfall is zero | none | `escrow funding planned`, `escrow starved`, `escrow full again` (Info), `funding guarantee broken`, `model amount cannot fund a full escrow`, `escrow budget reached`, `escrow funding plan skipped` (Warn) (`render_funding.go`); log keys `creates`, `retires`, `reasons`, `money_short`, `need`, `liquid`, `full_count`, `free`, `want`, `amount`, `counted`, `max_unsettled` |

## Absent subjects are omitted

A key is written only when its subject exists. `request finished` carries `escrow` only when the race picked one, `host` only for the crowned attempt, and `hosts` — every host tried, comma-separated — only when attempts ran and nobody was crowned. A burn carries no `nonce` when the session committed none, which only a session test double does. An empty value reads as a subject with no name, and a query for it matches every line that never had one.

## Which request a burn and a vote belong to

A burn names the request it was spent during under `burned_during_request`: the request a refused slot was meant for, otherwise the oldest request still waiting in the dispatcher's queue when a drain burned the nonce, or the request whose assignment arrived after it had left. The key is deliberately not `request`, because the nonce served nobody and a search for one request's own lines must not return it. Neither id is ever a metric label. The key is written only when the burn has a request to name. A failed timeout vote names its race's `request`; a warmup vote has none.

## Lifecycle lines outside a race

Host, escrow, chain and warmup transitions are decided in their own packages and written here. Each producer package declares a narrator interface over plain values that `*Journal` satisfies, so none imports `journal`; the composition root binds the journal where the producer is built (`SetNarrator`, `Deps.Narrator`, `Config.Narrator`, a dispatch observer method, or `publishEscrows`' function value `unservable`, the journal's `EscrowUnservable`).

A narrator method copies its arguments into a render closure and hands it to `emitLine`; the consumer runs the closure against the log sink. These lines feed only the log and each renders one message, so a typed payload per line would triple the code with no second reader. What to write — an omitted nil error, a Warn for a failed vote — is decided inside the closure, here.

| Producer | Narrator | Kind | Lane |
| --- | --- | --- | --- |
| `perf.Tracker` | `hostNarrator`, bound by `SetNarrator` | `KindHostTransition` | money |
| `limits.ParticipantLimiter` | `cutoffNarrator`, bound by `SetNarrator` | `KindHostTransition` | money |
| `engine` crown strikes | `crownNarrator`, part of `raceJournal` | `KindHostTransition` | money |
| `scheduler` dispatcher | `dispatchObserver.ExcludedHostServed`, through `tracedDispatches` | `KindExcludedHostServed` | money |
| `scheduler` dispatcher | `dispatchObserver.ForcedSend`, through `tracedDispatches` | `KindForcedSend` | money |
| `registry.Registry` | `escrowNarrator`, bound by `Deps.Narrator` | `KindEscrowTransition` | money |
| `publishEscrows` (`app/devshards.go`) | `unservable` and `unopened`, the journal's `EscrowUnservable` and `EscrowUnopened` | `KindEscrowTransition` | money |
| `escrow.Manager` | `lifecycleNarrator`, bound by `Deps.Narrator` | `KindEscrowTransition` | money |
| `chain.TxClient` | `settlementNarrator`, bound by `Config.Narrator` | `KindEscrowTransition` | money |
| `warmup.Prober` | `warmupNarrator`, bound by `SetNarrator` | `KindEscrowTransition` | money |
| `chain.PhaseObserver` | `healthNarrator`, bound by `SetNarrator` | `KindChainTransition` | money |
| `phaseNarrator` (`app/observers.go`) | none: the composition root may import `journal`, so it calls the chain methods directly | `KindChainTransition` | money |
| `escrow.Manager` funding planner (`planner.go`) | `lifecycleNarrator`, bound by `Deps.Narrator` | `KindFundingTransition` | money |

Host, excluded-host and chain transitions ride the money lane, because operators read them exactly when the consumer lags. Host and chain transitions follow edges — the host count and the backoff, and the chain poll — while an excluded-host serve and a forced send follow served nonces, so they move with the request rate; a forced send at most once per run of burns; their volume stays far inside `MoneyCeiling`.

Escrow transitions ride the money lane: `escrow settled` is the audit record of funds leaving and `settled escrow record dropped` names the only key that could settle an escrow, so neither may be dropped in a flood of progress lines. Their volume follows the escrow tick, far below `MoneyCeiling`.

## Nil errors are omitted

An error key is written only when there is an error. `escrow warmup found no nonce to spend` from a probe that failed without one carries no `error`, and `escrow warmed` carries `catch_up_error` only when the catch-up failed. A nil error written as a value reads `error=<nil>` in text and `"error":null` in JSON, which a search for failing warmups matches. The warmup's own vote is written at Warn when it failed, as a race's failed vote is, and at Info otherwise.

## Chain transitions

The chain observer and `phaseNarrator` keep deciding what moved, both on the observer's publishing goroutine: the observer keeps only whether the last publish was degraded — that is, carried a `LastError` — and narrates a turn to stale or recovered only when that flips, so one error followed by another is no turn; `phaseNarrator` compares epoch and phase, blocking state and reason. The journal only renders what it is handed. `publish` narrates health before it notifies subscribers, so the health line is queued before the epoch line of the same snapshot. `ChainEpoch` writes nothing for epoch 0, which only a poll that never read the epoch publishes — a silence rule decided inside its closure, like the ones `renderTimeoutVote` applies. `phaseNarrator` still records that snapshot, so the next one that carries an epoch is a change and is announced.

## Who may log directly

Lifecycle packages — `engine`, `scheduler`, `registry`, `escrow`, `perf`, `limits`, `chain`, `warmup`, `api` and `app/observers.go` — write no line themselves: a line written around the journal leaves out of order with the trace it belongs to and skips the lane that counts what was dropped. `guard_test.go` fails on any `logging.Info`, `Warn`, `Error` or `Debug` call there, under any import alias. `api/admin.go` and `api/errors.go` stay plain, because an operator's own action and a refused admin call belong to no lifecycle. Outside those packages `app/compose.go`, `app/lifecycle.go`, `app/devshards.go`, `app/runtime_params.go`, `app/session_retention.go`, `env/`, `nonces/` and `accounting/` log directly, none of them a lifecycle line — the refused warmup probe reaches the log through the journal's `renderProbeRefused`, not through `nonces` — and `internal/logkey/logkey_test.go` keeps checking their literal keys.

`guard_test.go` also fails when a producer package imports `journal`; each declares the narrator interface it calls. `keys_test.go` drives every exported producer method once with its widest input and fails on a rendered key `internal/logkey` does not declare, and on a producer method added without a sample, so the check cannot skip a new line. Its journal has a ledger that refuses the probe, so the refused-probe line is checked as well.

## Read next

- [`nonces/README.md`](../nonces/README.md) — the ledger sink.
- [`docs/operations.md`](../docs/operations.md), "Shutdown" and "Metrics".
