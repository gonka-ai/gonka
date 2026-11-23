# app

`app` is the composition root: it builds every layer package into one gateway process, starts it in the boot order and stops it in the shutdown order. Read it to find where two packages are joined, why the wiring runs in the order it does and what the admin operations guarantee. The operator's view of boot and shutdown is in [`docs/operations.md`](../docs/operations.md), "Boot" and "Shutdown".

The binary is [`../main.go`](../main.go): it copies the version the build stamps into `main.Version` to `app.Version` and calls `app.Main`. The stamp stays on `main.Version` because every build file sets `-X main.Version=…`, and `-X` on a symbol that does not exist is silently ignored.

## Files

| File | What it is for |
| --- | --- |
| `compose.go` | wires every package into one process |
| `routing.go` | builds the registry, the scheduler and the warmup together, because each needs the others |
| `capacity.go` | reads the chain snapshot into the numbers the limiters price by |
| `lifecycle.go` | starts and stops everything |
| `devshards.go`, `devshards_publish.go`, `devshards_republish.go`, `devshards_sessions.go`, `devshards_seeds.go` | turn stored rows into live escrow sessions |
| `runtime_params.go` | binds an escrow's heartbeat schedule to the chain's operational governance |
| `observers.go` | adapts one event into the several readers that want it |
| `planner_wiring.go` | feeds the funding planner the registry's escrow money and money-short wakeups, and exports its decisions and unsettled counts as metrics |
| `operations.go` | the admin operations behind [`api`](../api/README.md) |
| `model_filters.go` | the operator's per-model filter settings |
| `stop_token_vocabulary.go` | resolves the vocabulary the chain pins for a model's epoch, so the filters can check `stop_token_ids` |
| `prompt_tokens.go` | wires the [`tokenizer`](../tokenizer/README.md) library the context check counts prompts with, from the same chain pin |
| `served_binding.go` | counts each served-stream binding verdict, tells the engine the judged Finish applied and, on a mismatch, logs it and records it against the host |
| `tracing.go` | starts tracing and flushes it on exit |
| `session_retention.go` | removes escrow session directories the hosts' horizon has passed ([`docs/operations.md`](../docs/operations.md), "Session storage") |
| `session_storage.go` | removes a deleted escrow's session directory, and refuses any path outside the base storage dir or the base dir itself |
| `composed.go` | the surface the [`scenarios`](../scenarios/README.md) harness composes and boots the gateway through |

## Wiring order, and the knots in it

Most of `compose` is a straight line. Six places are not:

- **Logging is configured before anything else can log**, from `env.LogFormat`. JSON is the default: a collector reads its fields as labels, while the text line carries `log`'s own date prefix and would have to be re-parsed.
- **`serve` owns the signal context**, so releasing it survives a panic. `os.Exit` skips a `defer` left in `main`.
- **Routing joins the escrow set to the picker through the capacity model.** An escrow whose membership never reaches that model scores as weightless, is skipped by every pick, and serves nothing — so the join is not optional wiring. The warmup is handed the registry *after* the registry exists, because the registry publishes to it; and a nil warmup must never be assigned into the interface field, since a typed nil there is non-nil to a nil check.
- **The engine hears of an applied Finish through a registry built before it.** The registry's served-binding observer forwards every Finish it judges to `finishNotice`, and the engine is stored into that notice once it exists, so a served winner's held sample is decided when its Finish applies ([`docs/race.md`](../docs/race.md), "A served winner's sample waits for its finish"). A Finish judged before the engine exists has no held sample to decide.
- **The journal and the nonce recorder are built around each other.** The journal takes the recorder as its ledger sink when it is built; the recorder receives the journal as an argument of `Start`, which `serve` calls once both exist. `journalSettings` keeps a disabled recorder out of the journal's interface field.
- **The capture and cache collectors are registered after the server**, which owns both sinks. Nothing evicts capture files, so the refusal count is the only signal that capture has turned itself off at its byte cap.

## Two readers of one fact

Four small adapters exist because two subsystems want the same event for different reasons, and neither belongs inside the other:

- **`nonceAccountedRaces`** hands one race outcome to both readers of it. The metrics recorder asks how the fleet performed and is called on the spot; the ledger asks where each nonce went and receives the outcome through the [`journal`](../journal/), so the response path never takes the ledger's lock. The engine knows about neither. The warmup votes through the same poster the race uses and is counted by the same recorder, through `probeVotes`.
- **`tracedDispatches`** counts dispatch events on the spot and hands a burn, a tripped burn budget and a nonce spent on a host its request excluded to the [`journal`](../journal/), which writes their lines and the burn's ledger fact; the excluded-host serve has no counter, so its line is its only record. It wraps rather than living inside [`metrics`](../metrics/) because counting and narrating are different jobs, and it keeps the scheduler free of a logger. A burned nonce is logged with its nonce and *never labelled with it* — a counter keyed by nonce would grow without end.
- **`probeVotes`** counts a warmup's timeout vote like a race vote and hands it to the journal as a probe vote: it reaches the ledger without the race's `timeout vote failed` line, because the warmup writes its own.
- **`phaseNarrator`** turns the observer's five-second poll into a chain change only when something an operator cares about actually moved — the epoch or its phase, or requests blocked or unblocked — and hands that change to the [`journal`](../journal/), which writes the line. Subscribing the journal without it would write the same snapshot twelve times a minute.

## Relaxed mode, in one place

`modelCapacity` is one wrapper for both capacity readers, because the gauge must report the scale that admission actually applies, and only this type knows the operator's relaxed-mode override of the chain's raw blocking state.

Reading the chain's raw state instead would zero the scale during PoC, and a zero scale clamps every weight-derived cap to nothing — so relaxed mode would go dead in exactly the deployments that set an input-token cap or a per-model override, which are the ones that need it. `api.admission` calls the same method for the pre-queue gate, and the funding planner reads the same fold, so it creates escrows while admission lets requests through or only a confirmation PoC blocks them, in any `poc_mode`; it keeps its own pause for an epoch PoC, read from the raw `BlockReasonPoC`.

The per-weight *allowance* is the exception: it follows the raw chain phase rather than the override, because it bounds what the hosts can actually do.

## Shutdown

Thirteen steps, in a fixed order, described in [`docs/operations.md`](../docs/operations.md), "Shutdown". Every step runs even after an earlier one fails, except a step marked as needing a quiesced system — that one is skipped and the skip reported, because it destroys state the steps above it may still be using. A drain step is bounded by the shutdown budget without cancelling the work inside it.

Four positions in that order are load-bearing:

- **The nonce sweep stops before the escrow sessions close.** The step waits out the sweep in progress, so no sweep reads a closing session, and none runs on the live set `Registry.Close` empties, which would retire every escrow the ledger holds.
- **The journal closes after the escrow sessions and before nonce accounting.** Every producer that owns a shutdown step has stopped above it, and the ledger it drains into is still open; the warmup, which is only cancelled, and the republish after a devshard write can still narrate after it closes, and such a line is counted as a late event. Its close is bounded by the shutdown budget with a one-second floor, so a stuck sink cannot keep the ledger and the store from closing.
- **Nonce accounting closes after every emitter above it has stopped**, so the final snapshot holds the counters the run ended with rather than one taken while races were still classifying nonces.
- **Public-API connections close last.** Every step above can still reach the public API, and an idle socket closed under one of them is a socket the next poll has to re-dial. The chain's own gRPC connection is *not* closed here and cannot be: `common/chain` owns it and exposes no `Close`, so it lives until the process exits. That is why the tests ignore its goroutines rather than waiting for them.

Boot has a matching budget: the concurrent-build limit and the idle connection pool those builds reuse are sized together, so the pool is neither starved nor larger than the builders can use.

## Escrow sessions and the chain connection

`chainBackedSessions` owns the chain connection because it is the only provider that needs one — an in-process provider dials nothing, which is what keeps a test from carrying a live gRPC client. `sessionSources` is a parameter rather than a constant so the transport an escrow is served over is chosen once, here; the chain access travels with the sessions because it rides the same connection, and `Reader` and `Transport` are interfaces so a provider that dials nothing can answer them in process.

- **The bridge is one object for the process.** It holds the chain client every session reads escrow state through, so building one per session would open a connection per escrow and lose the client's cache.
- **Production does not use upstream's `NewGRPCBridgeFromURL`**, which is its test constructor. The bridge is built over a client carrying the CometBFT RPC fallback: every read survives the gRPC endpoint being unreachable, and the create, rotate and settle transactions broadcast once over the transport those reads selected ([`chain/README.md`](../chain/README.md), "The gRPC transport"). An empty RPC endpoint lets `common/chain` derive one from the gRPC host at the standard port, which is how a default deployment is laid out — a deployment that moved it has to say so, or the fallback resolves to a host nobody is listening on and dies silently.
- **The runtime-params feed rides the same connection**, because it asks the chain the same questions the bridge does. It answers what schedule an escrow's heartbeat runs on, and it cannot starve one: a chain it cannot reach is not an error but an empty snapshot, and zero on the wire means keep the compiled schedule. It holds its own context rather than the boot one, so the shutdown step owns its lifetime and a signal does not cancel it before that step is reached — the same reason `heights` builds its block feed that way ([`app/runtime_params.go`](./runtime_params.go), [`docs/escrows.md`](../docs/escrows.md), "Height sync").
- **Seeding leaves a devshard it already knows alone**, so a restart cannot resurrect one an operator deactivated. Settlement deletes the row of an escrow it settled, so the seed writes that escrow back on the next boot, and publication refuses it.
- **Publication follows the store's `active` flag**, builders at a time; an escrow whose key or record is missing is marked inactive rather than failing the boot, and one the chain reports settled has its row deleted, as settlement deletes it ([`docs/operations.md`](../docs/operations.md), "Boot"). A write to a devshard row wakes a republish, because the rotation lifecycle owns those rows and knows nothing about the registry. `depletionNotice` breaks the reverse cycle: the manager settles through the registry, so it cannot also be constructed before it.
- **Importing an escrow copies its storage rather than referencing it**, so the gateway owns the only handle to what it serves. Only regular files are copied — session storage is a flat set of SQLite files, so a directory below it is not part of the escrow.

  Every refusal the row can answer up front runs before the first write, so such a refused import leaves the storage it names as it was: an id that is not a chain escrow id in canonical decimal (`ErrInvalidEscrowID`, HTTP 400), which is also what keeps `escrow-<id>` inside the storage dir; a key variable that does not resolve; a parked row (`ErrDevshardNotActivatable`, HTTP 409); and an escrow whose row is active or whose session the registry still holds, published or draining (`ErrDevshardExists`, HTTP 409), whose files that session will still write. A park that lands while the files are copied is still refused, by the row write that follows the copy (`ErrDevshardNotActivatable`), with the files already replaced.

  An inactive row may be imported again, and its files are replaced. A registration (`POST /v1/admin/devshards`) and a seed in `DEVSHARDS_JSON` refuse the same ids with the same `ErrInvalidEscrowID`, the seed by failing the boot, because their session opens storage at the same `escrow-<id>` path.

## Admin operations

`suspiciousHosts` is the operator's manual pin list, cached in memory because every race reads it and backed by the store because a pin outlives the incident that prompted it. **The store is written first**: a pin the gateway acts on but forgets on restart is the failure an operator cannot see.

`modelFilters` holds the operator's per-model filter settings the same way, store first, and serves the chat path an immutable profile set it replaces whole on every write, so a request never reads half a change. A stored row it cannot read at boot is skipped with a warning and the model runs its built-in profile: configuration never stops the gateway from starting.

- **`Deactivate`, `Settle` and a registration that writes an inactive row stop routing before the row changes**, so nothing new is admitted while the write runs. `Deactivate` and the registration retire the escrow again once the row is inactive, so a republish pass that read the row active cannot leave it routed. An import refuses an escrow whose session the registry still holds, published or draining, so it never overwrites storage a session will flush.
- **Operator writes to one escrow run one at a time.** `Activate`, `Deactivate`, `Settle`, `DeleteDevshard`, a registration and an import hold a lock per escrow id from their checks to their last write, so a concurrent call never leaves the row active while the escrow is unrouted, an import copying over storage a registration just routed or a settlement is reading, or a delete removing a row an `Activate` just routed. `Settle` stops routing, parks the row and stops routing again, and holds the lock until its settlement returns, which the admin settle routes bound with a 5-minute timeout. `Activate`, `Deactivate`, `DeleteDevshard` and `Settle` answer 404 `unknown devshard` when a settle they waited behind deleted the row; a registration and an import write the row again instead. `Activate` refuses a parked row.
- **`DeleteDevshard` refuses what a settlement or a session still needs.** It re-reads the row under the lock and refuses a row parked for settlement (`ErrDevshardBusy`, HTTP 409), whose storage the settlement reads, and an active row or an escrow whose session the registry still holds, published or draining (`ErrDevshardBusy`, HTTP 409); only then does it delete the row and the storage. Any other failure, of the store or the disk, answers 500.
- **`Activate` refuses an escrow parked for settlement.** Its balance is already committed to a settlement that is either on chain or on its way, and serving from it again spends nonces that settlement does not account for.
- **Every key is named, never carried.** `CreateEscrow` takes the name of the variable holding the signing key. See [`docs/operations.md`](../docs/operations.md), "What is exposed".

## The harness surface

`composed.go` exports what the [`scenarios`](../scenarios/) harness needs: compose the real gateway over a fake chain and in-process hosts (`Compose`, `Sources`), boot it one step at a time inside a `testing/synctest` bubble (`BootSteps`, `BootStep`), read its parts (`Handler`, `Config`, `Store`, `Observer`, `Escrows`, `Manager`, `Races`, `Nonces`, `Journal`, `Telemetry`), act as the operator (`Deactivate`, `Settle`) and stop it (`Shutdown`, `ShutdownGracePeriod`). Nothing in the binary calls it. It holds adapters only: a change of behaviour belongs in the file the adapter reaches, never here.
