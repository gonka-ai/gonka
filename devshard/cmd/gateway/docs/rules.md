# Rules

Three kinds of rule govern this gateway, and they answer three different questions:

- **Invariants** — what must never stop being true. Break one and the gateway still runs; it just loses money or lies.
- **Non-goals** — what it does not do, so "is this missing or intended?" is answerable without reading code.
- **Verification limits** — what a green test suite does *not* prove.

The strongest invariants below are enforced by a type or a lock; the rest hold only while every caller honours them.

## Part 1 — Invariants

### 1. A committed nonce is always settled

Advancing the nonce sequence sends a `MsgStart`. From that moment the inference is owed a resolution: either the host finishes it and the session records the finish, or the gateway posts a timeout vote. A committed nonce with neither is an orphaned chain message settlement can never resolve — money the escrow can never reclaim.

Every committed nonce ends in exactly one of three states:

| State | Who resolves it |
| --- | --- |
| Served | an attempt ran; the response was applied, or a timeout vote was posted |
| Ghosted | the scheduler burned it locally with a recorded reason; nothing was sent, so nothing is owed |
| Stranded but voted | the assignment could not be dispatched; a synthetic outcome carries it into the timeout plan |

What enforces it:

- **The decision type is closed.** `scheduler.Decision` is a sum of `serve`, `burn`, `hold` and `decline` (`scheduler/decision.go`). There is no way to express "committed and nobody's" without changing the type.
- **Terminal events block.** `AttemptDone` uses a blocking send while progress events are dropped when the coordinator is busy (`engine/attempt.go`, `emit` vs `offer`). Making the terminal non-blocking would compile, pass most tests, and quietly break this invariant.
- **An undispatchable assignment is stranded, not dropped.** `strand` releases the host slot, keeps the escrow hold, and appends a synthetic attempt with `TerminalNoReceipt` (`engine/race.go`).
- **A race that fails before its first attempt still reports** (`engine/race.go`, `fail`).
- **`Engine.Stop` is a real barrier.** A race is registered under the engine mutex *before* it starts and released only after every vote it owes has been posted (`engine/engine.go`, `admit` and `raceRegistration.release`). Registering later would leave the just-admitted race — precisely the one still owing a vote — outside the barrier.
- **`Run` recovers a panic solely to release that registration, then re-panics with the same value.** Without it a panic between admission and settlement leaves `Stop` waiting forever.

**Seven shapes this invariant fails in**, each a variation of *state moving between the commit and the lookup meant to resolve it*: admission that peeks instead of acquiring atomically; a missing dispatch target that returns early after the commit; a declined-admission path nothing reaches; an escrow retired between race end and settle, leaving the settlement unresolvable; an in-flight counter no production caller feeds, leaving every escrow reading idle; a reply applied only on the success path, stranding nonces that failed *beside* a reply; and a re-added escrow id taking the settlement lookup from the entry still draining.

### 2. Exactly one outcome and exactly one winner per race, on every path

A race runs several attempts, may be abandoned mid-flight, and continues after its caller returns. Every exit path still produces exactly one `RaceOutcome` and crowns at most one winner.

**One winner.** `c.winner` is assigned in `answer` and `settleClaims`, both guarded by `c.winner == nil`, both on the coordinator goroutine. Attempts do not decide — they *claim*: a writer with content sends a crown request and blocks on the reply (`engine/stream.go`, `winnerWriter.claim`), so no byte reaches the client before the coordinator has answered. A loser's `Abandon` clears its client field, so a suppressed attempt has no reachable sink at all rather than a sink behind a branch someone must remember to write.

**One outcome.** `report()` closes the race's `done` channel before building the outcome. A second call panics on the double close — a duplicated outcome takes the process down at the point of the defect instead of passing silently.

**One recording point.** The exemption ladder deciding whether a host is judged is applied in `Engine.record` and nowhere else.

### 3. No field crosses a goroutine except through the event channel

A struct shared across goroutines on the money path is a data race deciding what gets paid: the drain can return while attempts are still writing the fields the outcome then reads.

Here each attempt owns its state and publishes facts as events. `attemptState` is never read by another goroutine; `liveAttempt` is written only in response to events, on the coordinator; the byte path runs only on the attempt's goroutine. There are exactly two cross-goroutine channels — the event channel and the crown handshake — plus a close-only `done`.

**`select` chooses at random among ready cases, and that forced a specific shape.** When a buffered event and a fired timer are both ready, the coordinator may take the timer and act on state the queued event has already invalidated: spending a nonce on an escalation newer state has cleared, mislabelling a healthy host, reporting a completed winner as cancelled. So every select arm that *reads* race state first drains the event queue (`engine/race.go`, `catchUp`). Its absence is intermittent: the race passes at `-count=3` and then reports the wrong terminal state.

Precedence is therefore explicit, never inherited from arm order: `nextDeadline` takes the earliest deadline outright, and the declaration order of the trigger constants breaks exact ties (`engine/deadline.go`).

### 4. Routing and settlement read the escrow set asymmetrically

```
Routing      → published entries only
Dispatch     → published, then draining, for a nonce the pick already committed
Settlement   → published, then draining
Status reads → published or draining, without counting
```

Dispatch is the step between the two: the pick committed the nonce on a published escrow, and a rotation in the moment before the race resolves its target must not strand it, so the target is taken with a request hold on the published or the draining entry (`registry.AcquireCommitted`; `api/dispatch.go`, `Sessions.Target`). The assignment's own hold keeps that entry from closing until then.

Routing must refuse a retired escrow, to every new request. The one exception is a race's own escalation on an escrow retired for its nonce cap, while that race still holds it and below the escalation bound ([routing.md](./routing.md), "Picking an escrow"): the race's hold keeps the escrow busy, so nothing that waits for its drain has finalized it. The nonces it already committed still owe votes, and the draining entry holds the storage those votes settle through. Unifying the two lookups — the obvious tidy-up — either dispatches into an escrow that is going away or strands the votes of one that already did.

The narrow interfaces make the mistake hard to express: swapping one lookup for the other at the API layer does not compile, because that layer declares only the settlement lookup. The read-only status handle is a third case precisely because it must **not** take a hold — an operator inspecting an escrow must not keep it from retiring.

### 5. The slot and the escrow hold are taken with the nonce, and given back after the vote

Three resources move together: the nonce, the participant's concurrency slot, and the escrow's in-flight hold.

**Acquisition is one atomic step**, inside the callback of `session.Advance`, under the session's own lock with the nonce-to-host binding already fixed (`scheduler/dispatcher_queue.go`). A refused slot becomes the burn its refusal names: `ghostWindowFull`, or `ghostCutOff` when a forced send was refused too.

**Release is by whoever spends it.** `Acquire` hands back a lease that releases exactly what it took, exactly once, and the assignment carries it to the attempt; the engine's view of the limiter can neither acquire nor release, so an interface that could pair them wrongly does not exist. Every scheduler path that cannot deliver an assignment gives the slot and the hold back *together*. A burn takes the hold and no slot, and gives the hold back whether its commit succeeds or fails.

**The hold outlives the race**, released only after the settlement vote is posted, from inside the goroutine that posts it. The exception is the stranded case, where the assignment's own hold is kept, because the escrow being retired is exactly why there was no target and the vote still has to reach it.

**The peek is a filter, never the authority.** `limits.Admits` is a cheap pre-filter for routing, where a stale answer costs nothing; used as the authority it commits a nonce the acquire then refuses. It names why a host would refuse, because only one of the two reasons may be crossed; `limits.Available` is the same peek as a yes or no, for the capacity weights.

### 6. Boot and shutdown order is a contract

Nine steps up (`app/lifecycle.go`, `bootOrder` and `startAll`) and thirteen down (`shutdownOrder` and `stopAll`), both listed in [operations.md](./operations.md) and both read back by a test, because an order nothing pins is a comment. Going up, the chain observer starts before anything that scores a host, the nonce ledger before the emitters that file facts into it, and the HTTP listener last, so the first request meets a gateway that is fully assembled; a step that fails stops the boot where it failed. The order encodes five dependencies: races drain to the vote that settles their nonces, and that vote needs the escrow sessions, the observer and the chain client alive — so races stop second and everything they depend on stops below them. The store is second to last because closing it drains the accounting ledger. Chain connections close last because every step above can still reach the chain. The journal closes after every producer that owns a step above it, the escrow sessions' diff observer last, and before nonce accounting, the ledger it drains into. The nonce ledger's sweep stops, and the sweep in progress is waited for, before the escrow sessions close, so no sweep reads a closing session or retires the escrows `Registry.Close` takes out of the live set. Work that outlives its step or has none — a timeout vote still posting after the races drain ran out of budget, a handler still running after the listener's shutdown, the warmup, the republish after a devshard write — can still narrate after the journal closes, and such a line is counted as a late event.

Three runner properties are as load-bearing as the order:

- **Every step runs even after a failure**, because the store must be reached whatever happened above it.
- **The drain is bounded but not cancelled.** Cancelling aborts the vote the drain exists to protect; waiting forever forfeits every later step to the SIGKILL that follows. An overrunning drain is left running and reported (`app/lifecycle.go`, `waitFor`).
- **One step is guarded by quiescence.** A step marked `needsQuiesced` is skipped, and the skip reported, whenever anything above it failed — the escrow sessions are the only such step. Closing a session takes no lock, so closing one under an overrun drain races a nonce commit, trading a vote already going to be dropped for on-disk state nothing can repair.

Two failures this order and this runner prevent: omitting the dispatchers and the registry closes the gateway store while every escrow session still holds a handle; a `waitFor` that discards its context confines the grace period to the listener — one unanswered request plus SIGTERM blocked the drain for minutes and the orchestrator killed the process mid-vote.

### 7. Money-path orderings

Each is an ordering whose reversal produces a plausible-looking program that loses funds.

| Rule | Enforced at | Cost of reversal |
| --- | --- | --- |
| Durable intent is written before the broadcast; a failed write aborts before broadcasting | `chain/txclient.go`; `escrow/commitments.go`, `createFor` | a crash between broadcast and record leaves a funded escrow nobody knows about |
| The precomputed tx hash must equal the one the node returns | `chain/txclient.go` | the recovery record points at a transaction that is not the one that landed |
| The commitment row is deleted only after the escrow is registered | `escrow/commitments.go`, `persistEscrow` | a crash in between loses the only pointer to a funded escrow |
| A transaction that may still land is never dropped from durable intent | `escrow/commitments.go`, `commitmentReconcileGrace` | an escrow created but not yet indexed is forgotten |
| An escrow is deactivated only on confirmed absence; any endpoint error keeps it active | `escrow/checker.go`, `TriggerEscrowCheck` | a network blip deactivates a live, funded escrow |
| Park before the settlement reconciliation, not after | `escrow/settlement.go`, `settle` | the row is deleted while the escrow is still routable, and nothing can un-publish it |
| `settlement_pending` is cleared only after the broadcast is confirmed | `escrow/settlement.go`, `settle` | a settlement that failed looks completed |
| The row is deleted only after settlement succeeds; with settlement off the escrow is parked, never deleted | `escrow/settlement.go`, `retire` | the row names the env variable of the settling key — delete it and the funds are unrecoverable |
| A nonce-capped escrow is retired; a balance-short escrow keeps serving what it can pay | `escrow/planned_lifecycle.go`, `drainPlannedMarks`; `scheduler/escrow_pick.go`, `exhaustionReason` | an escrow past its nonce cap stays published and fails every request routed to it |
| A zero-row UPDATE or DELETE is an error, never a silent success — except `ParkForSettlementIfActive`, for which zero rows is an answer, not an error | `store/devshards.go`, `requireOneRow` | state diverges from what the caller believes it wrote |
| Private keys are never persisted — only the name of the variable holding one | `store/devshards.go`; `api/errors.go`, `ErrPrivateKeyEnvRequired` | a key reaches the row, the logs, and every operator in between |

### 8. Fail-closed and fail-open are chosen per signal

Neither default is right everywhere. What matters is that each is decided rather than inherited.

**Fail closed** — refuse rather than guess:

- A model absent from a *populated* per-model weight view gets zero capacity rather than inheriting the all-model view (`limits/capacity.go`). Model strings are client input; without this an unknown model inherited full network capacity.
- A NaN or out-of-range scale clamps to **zero**, never to one (`limits/gateway.go`, `clampUnit`). Returning an empty weight map instead hits the "no baseline means unlimited" branch and grants *full* capacity.
- An empty escrow registry means not ready, not "everything routes": 503.
- A host's node-version capability is unknown until proven, and a stale entry counts as unknown.
- Admin routes do not exist without a configured key — 404, not 401 — and a key must be at least 16 characters.

**Fail open** — carry on rather than break a working system:

- A failed chain poll republishes the previous snapshot with `LastError` set rather than an empty one.
- `MaxNonce == 0` means "not fetched", not "no limit": the scheduler falls back to `fallbackNonceCeiling` (19 800) rather than disabling the cap, and reports no escrow exhausted against it, because the fallback is not the hosts' cap and a reported escrow is parked.
- A nil preserved set means "not loaded yet", so everyone counts as preserved. Reading absent data as "nobody is preserved" would ghost every nonce the escrow owns.
- A model without a tokenizer the gateway reads, or whose tokenizer is still loading, is not checked against its context length: the prompt goes to a host, which is how every prompt went before the check ([request.md](./request.md), "Step 6 in detail").
- With no weights reported at all, escrow scoring falls back to availability-filtered membership share instead of zero, which would reject every request during the boot window. Because that fallback serves requests correctly and *silently*, it publishes a gauge derived from the same two predicates the branch reads, so the signal cannot drift from the behaviour it reports.

### 9. Bounded by construction

Loops that spend money or memory per iteration carry an explicit bound, not a hope:

| Loop | Bound |
| --- | --- |
| the scheduler's drain | predicates frozen for the drain, a burn budget of `groupSize × (waiters + 1)` fixed at entry |
| SSE reassembly | three budgets — per attempt, per participant, process-wide; the participant level stops one host that never terminates an event from starving every other host of classification |
| the accounting ledger | rejects a zero on either retention axis, and sheds rows rather than blocking the response path |
| settlement of parked escrows | `pendingSettleBudget` = 4 at once outside the margin, `urgentSettleBudget` = 8 at once inside it |
| version polling | 16 ways, 2 s per fetch — a sequential pass over hundreds of miners exceeds the freshness window, which silently makes every node report as not validation-capable |
| boot | escrow runtimes built under a semaphore whose depth is also the idle-connection pool size |

Three maps never evict, and each has a reason eviction would break:

- **Crown strikes** — the entry *is* the running count of contentless answers, and only an answer that carried content clears it; a dial failure or a stranded nonce says nothing about the host, and evicting on one hands it a clean record it did not earn.
- **Reassembly charges** — held by pointer for the life of the attempt, so dropping the entry mints a second counter while the first still carries live charges, and the participant's cap then bounds nothing.
- **The per-escrow opening lock** — removing a lock while a caller waits on it lets the next caller take a fresh one and open the same SQLite file concurrently, which is the failure the lock exists to prevent.

Both of the first two are keyed by participant, and the participant set is the bounded validator set.

### 10. Counters saturate

A counter that wraps is worse than one that stops, because the value it wraps to is a number every comparison downstream believes. A host's in-flight token count that wraps negative reads as a window with room, and the limiter admits without bound; a window sized by multiplying a request count by a model's context length wraps into a negative window that never grows again.

So every counter an admission or a window decision is read from, and every total one request's record carries, is added and multiplied through `internal/safemath`, which stops at the bound instead of passing it: a client's `max_tokens` and the in-flight tokens it becomes, a context length governance published, an operator's configuration, and the output tokens a host reports for one request (`limits/participant.go`, `api/accounting.go`). The ledger's roll-ups across rows are outside the rule — they aggregate what is already recorded, for a report nothing admits or refuses on. What is compared rather than stored is compared in `float64`, where a value near the `int64` ceiling widens instead of wrapping.

The two other defences are the bounds themselves. `Validate` refuses a configuration whose knobs could be multiplied past the ceiling, and refuses a float that is not finite, because `NaN` is false in every ordering comparison and passes a range check written as one. `contextWindowsOf` drops a context length outside the range a model could plausibly have rather than clamping it, so a model governance reports nonsense for keeps the length it was last priced at — the fallback, where it has never had one — instead of being priced at the ceiling.

Money is the exception: `safeMul` in the escrow pick and `safeAdd` in the state machine report an overflow rather than saturating, because a reserve that saturates is a reserve the chain will not honour. There the right answer is to refuse the request, not to serve it at the wrong price.

### 11. Labels, ordering and determinism

- Metric label **values** are exported by the package that emits them and referenced by the metrics layer, never restated. Renaming the constant breaks the build rather than the dashboard — but editing its *value* still compiles and moves the wire string under every panel and alert, so those constants are a contract with Grafana, not an internal vocabulary.
- A route label is always a route pattern, never a raw path: a raw path is an unbounded label, and on unauthenticated traffic it is one series per probe.
- **Escrow id is safe on gauges and expensive on counters.** Escrow ids are monotonic and never reused, so a counter keyed by one grows with uptime and no scrape retires it. Exactly three counter families carry it — ghost burns, nonce holds, burn budget — because a spent nonce must be attributable to the escrow that paid for it, and the scheduler deletes all three when it reaps that escrow's dispatcher.
- Iteration with an observable effect is over **sorted** keys: membership publication, the escrow-missing drain, the registry's snapshot and close paths.
- Request capture admits a deterministic **stride**, not a random sample, so a configured rate is honoured exactly and reproducibly.

### 12. A dependency set is constructed, never assembled

A package that takes a `Deps` refuses one it cannot work with, and says which field is missing: `api.New`, `engine.NewEngine`, `scheduler.NewScheduler`, `escrow.NewManager` and `store.NewLedger` all return an error rather than a half-wired object. Without that, a forgotten dependency is a nil interface the compiler accepts, and it surfaces as a panic — in the constructor for the engine, on the first request for the scheduler, on the first tick for the escrow manager.

Optional dependencies stay optional and are guarded at every use: metrics, the ledger, the timeout poster, the dispatch observer, the engine's journal. The constructor is where that distinction is written down, so a reader can tell "not wired yet" from "wired to nothing".

The rule does not reach values. A struct the caller fills with already-checked numbers stays a literal, and the one that consumes it validates: `store.Retention` is two fields, and `NewLedger` refuses a non-positive age or a row cap below one. A constructor there would duplicate a check that already exists closer to use. Snapshots and events a package hands *out* — `perf.HostState`, `limits.HostWindow`, `engine.TimeoutEvent` — are literals for the same reason: nothing about them can be wrong at construction.

## Part 2 — Non-goals

### Not provided

| Not provided | Why, and what stands in its place |
| --- | --- |
| **Top-level routes for a single escrow** | one escrow is a pool of one; everything is reachable at `/devshard/{id}/…`, so an operator script names the escrow id |
| **A hand-written OpenAPI document and Swagger UI** | a hand-written document drifts from the routes with no test to catch it, and the UI pulls assets from a CDN. Shipping a stale hand-written document is worse than shipping none |
| **Short-content response capture** | capture has two triggers only: a request the filters rejected, and one every attempt failed. What answers the same question is counted rather than kept — crown strikes, `attempt_failures_total{visibility="no_winner"}`, the per-request record |
| **A configurable host route prefix** | derived from the binary's own version and not overridable (`app/compose.go`, `ResolveRoutePrefix`). The prefix names the protocol version sessions are created with and settlements carry; a knob that sets them apart is a way to build a settlement the chain will not take |
| **A quarantine state machine** (timed sentences of 30–60 minutes) | per-host congestion windows plus a cut-off whose worst case is minutes: adaptation instead of punishment. The operator escape hatch is `POST /v1/admin/participants/unquarantine` |
| **A pairwise speed comparator and a winner hold** | the gateway has no pairwise routing, so there is no preference signal to hold for — and inline in the writer a hold stalls the eventual winner's own socket while buying nothing |
| **Host-index-keyed performance metrics** | `host_idx` is not an identity; performance families are keyed by participant |
| **`devshard_runtime_reserved_tokens`** | token reservation lives in the limiter |
| **Probe attempts inside the race engine** | unreachable: a nonce that cannot be served is burned inside the scheduler and never becomes an attempt. There is no probe field and no guard for one; the concept survives as ghost burns |
| **Escalation to a measurably faster secondary** | it needs a latency model this gateway does not keep, and a faster host is not a reason to abandon an answer already streaming |
| **Persisted participant health** | congestion windows, cut-off state and decayed counts all start empty. Minute-scale backoff self-heals faster than replaying stale penalties is worth. The cost is honest and small: a genuinely bad host gets one free window after every deploy |
| **Migrated state** | state starts fresh, bootstrapped from `DEVSHARDS_JSON` and the admin import endpoint |
| **A `capacity_aware_limits` toggle** | the weight model is always on |

### Refused mechanisms

**No panic-recovery middleware.** `net/http` already recovers a per-connection panic, and a recovery wrapper would be free to write a JSON error object into a half-written SSE stream. The one hazard recovery would genuinely fix — a panic between a race registering itself and releasing that registration — is fixed inside the engine by a deferred recover that releases through an idempotent token and re-panics with the same value.

**No per-request cost accounting.** The request ledger records tokens and topology, not money. A breakdown computed as a read-time join against live session state would print zeros once an escrow settles. Money lives in chain settlement; the ledger answers "what did this request do", not "what did it cost".

**No KV-cache affinity routing.** `scheduler.AffinityHint` is an empty struct with no caller — the extension point exists so adding affinity later is a local change inside the scheduler rather than a new selection mechanism bolted beside it.

**No data-driven escalation beyond the first-token budget.** The escalation curve itself is a hardcoded quadratic in prompt size. One measured value stretches it: a host's own p75 to first content, which can only *extend* the budget, never shorten it (`engine/escalation.go`, `firstTokenBudget`). Nothing else in the ladder reads a percentile.

**No cached reply behind any refusal but proof-of-compute.** A hit needs no host, no nonce and no limiter slot, so while proof-of-compute blocks requests the gateway still answers the requests it has already answered, and refuses every miss as before. Proof-of-compute outranks a stale snapshot in `admission`, so a snapshot that went stale while it was blocked still serves hits — a cached reply does not depend on the chain. A stale snapshot outside proof-of-compute serves none, and the kill switch refuses before `chat` runs (`api/admission.go`, `blockedOnlyByPoC`).

### Boundaries it declines to cross

- **The engine never acts on escrow lifecycle facts; it reports them.** An escrow a host reported missing is recorded on the outcome and handed to the escrow manager, which verifies against the chain and decides. That is what keeps `engine` from importing `escrow`.
- **An exhausted deposit is reported by the scheduler, not the engine.** A request whose pick failed may never have been assigned an escrow to name; the dispatcher is the one seam holding the failure and the escrow id at the same moment.
- **The gateway does not enforce the economics.** It caps `max_tokens`, so the reservation is bounded by what was asked for, and the chain clamps actual cost to that reservation. Within it a host can still over-claim; the defences are on-chain response-hash validation and the permanent state-root-divergence block.
- **The gateway does not restate chain policy locally.** The maximum active nonce count is a governance parameter. A failed read falls back to a conservative constant; it never invents a policy of its own.
- **No changes to the shared `devshard/` packages, except where money is lost there.** The rule holds for anything the gateway can normalise at its own boundary. It has four exceptions, all defects that lose money and which no boundary can normalise away: three whose cost is a nonce settled at full reserve — the execution-timeout sweep, the confirm stamp read from the record rather than the session's cache, and the sentinel that names an unposted vote — and the transport's SSE event cap, which, set below what a host may send, refuses an honest host's whole answer after its nonce is committed (see [`findings.md`](./findings.md)). Each fixes the shared packages for every binary that uses them, which is the test for whether the exception applies. The standing example of the rule itself: the shared timeout handler returns a non-nil error on its own success path, so a posted vote is recognised by the handler's `Applied` flag rather than by the error (`api/timeouts.go`, `engine/session.go`).

### Known gaps

Each entry below is a capability this gateway does not have, and the reason it does not.

- **One per-escrow recovery tool is not served**: `sync-hosts`. The read-only half of the recovery surface **is** served, and resolves through the settlement lookup so a draining escrow still answers — which is the point, since the escrow needing inspection is usually the one in trouble. `signatures/collect` is served as the one write on that surface: it asks the group again for the signatures of one nonce, into the session a settlement builds from.
- **`/v1/debug/perf` is not served.** The request ledger at `/v1/requests/{id}` answers the same question per request, and the accounting findings carry the thresholds a host's numbers are judged against.
- **No test reads the dashboard.** [`gonka-gateway-escrows.json`](../../../../deploy/join/observability/grafana/dashboards/gonka-gateway-escrows.json) is built for this gateway and every family and label value it queries is emitted, but nothing checks that automatically, so a change to a family is checked against it by hand, with `jq -r '.. | .expr? // empty'`. `gonka-gateway-observability.json` queries nine families this gateway does not emit — capacity scale, participant limit rejections, quarantine state, picker choice, slot decisions, the skipped-escrow gauge, critical user failures, no-winner attempts and user-visible wins — and is not this gateway's dashboard. The repository carries no alerting rules.
- **A per-participant decay half-life is captured when a host is first seen**, so changing it at run time reaches only hosts seen afterwards.

### What a test stand may reach

Three of the bounds a nonce is judged against are out of reach of any test that runs in minutes: an attempt that was sent and went silent is held for `streamingHardTimeout` (20 minutes); the chain's own execution deadline is half an hour, and nothing votes a timeout before it; and a nonce is not classified until a timeout outcome is recorded for it. Without a way in, the whole expensive-failure path — `unfinished_execution`, applied timeouts, the sweep that retries a vote, the chain cross-check — is unprovable end to end.

So a stand may shorten them, and only a stand. The values arrive as `engine.Deps.E2E` and as the session's protocol deadlines, loaded once by [`env.LoadE2E`](../env/e2e.go) and handed to the session, and every one of them is refused by the shared `devshard/internal/e2econfig` unless the process set `DEVSHARD_E2E=1`. Three properties make that safe, and all three are load-bearing:

- **An operator gets no knob.** The values are not in `config.Config` and cannot be set through the admin settings endpoint, so "backstops are not tunable" stays true of every deployment.
- **A refused value cannot stop a boot.** `LoadE2E` returns an empty struct and warns rather than failing, so a stray variable in someone's environment can neither change how a deployment behaves nor keep it from starting.
- **Zero is the production bound.** Each field defaults to the compiled constant, so a stand that sets nothing measures exactly what ships.

## Part 3 — What the tests do not verify

The end-to-end suite is [`devshard/e2e`](../../../e2e) (`gateway_*_test.go`): the gateway as its shipped container beside three real host containers and a mock-chain process, reached over real HTTP and real gRPC.

A green run of that suite does not verify the following. These are not gaps more of the same suite closes; they are outside what the stand can reach.

**That the chain's rules accept the gateway's transactions.** The wire is real: the stand decodes a signed `TxRaw` with the generated cosmos and `inferencetypes` types, so a wrong field number or type URL on `MsgCreateDevshardEscrow` or `MsgSettleDevshardEscrow` fails it. What stays modelled is everything the chain then *decides* — gas, fee denomination, unordered-transaction TTL, sequence handling under contention, the escrow-created event shape. Every chain response is still written by the stand, and that remains the largest irreducible risk.

**That real hosts behave like these.** The hosts are the real host binary, but their inference engine is a stub. A vLLM version emitting a new SSE field, a valid receipt followed by a stream truncated at an unscripted byte boundary, TCP behaviour under packet loss — all unrepresented.

**That the sweep is what posted a vote.** The execution-timeout path is reachable on the stand — the deadlines arrive as `DEVSHARD_E2E_*` and a stalled nonce settles in seconds rather than half an hour — but the stand cannot attribute the vote. Leave the race alone and it votes first; restart the gateway to take the race away and the nonce ledger, which lives in memory, starts empty, so the vote that follows has nothing to be counted against. The sweep is proven in `user` and `state` instead, including a vote its verifiers actually apply.

**Anything below the volume floor.** A finding needs 20 nonces from one participant, so a scenario spending a handful raises none however extreme its failure rate. The stand can show a rate is computed; it cannot show a finding fires.

**Any tuning threshold.** Peak-EWMA, the hedging trigger, outlier ejection and the congestion windows all take a latency distribution as input. "Given these numbers, this decision" is asserted in the owning packages; "these thresholds are right for the fleet" is unanswerable without production traffic.

**Real concurrency at real scale.** The widest race in the stand is single digits. One scale hazard is known and needs load to observe: `ProcessResponse` serialising a wide race on one session mutex.

**Settlement money end to end.** The suite asserts the payload the gateway builds. Whether the chain then pays the right participants the right amounts is chain-side.

**Byte-for-byte compatibility with real clients.** The frames cross a real socket, so their bytes are the bytes. Whether an OpenAI-SDK or aiohttp client in the field parses them as intended is still an assumption, not a result.

**In one sentence:** the stand verifies every decision the gateway makes given a set of inputs, the wiring that makes those decisions reachable, and the bytes it puts on the wire — not that its model of the chain is correct, not that a real host answers like the stub, and not any tuning.
