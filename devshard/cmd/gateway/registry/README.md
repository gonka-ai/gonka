# `registry` — the live escrow set

Which escrows exist right now, what each serves, and who its hosts are.

## What it owns

- **The set itself** (`registry.go`, `escrow.go`) — escrow id to session, model, group, published or draining, behind a copy-on-write snapshot so a reader never blocks a writer.
- **Sessions** (`session.go`) — two kinds. A serving session has host clients and can dispatch; a read-only one rehydrates from local storage alone and can build a settlement but can neither serve nor finalize.
- **Membership and capacity** (`membership.go`, `views.go`) — the participant set each escrow contributes to the capacity model, and the in-flight count routing scores by.
- **Where the hosts are** (`views.go`, `HostDials`) — each live escrow's reachable addresses, deduplicated per address, so an observer such as [`hostping`](../hostping/README.md) reads the live set instead of keeping a mirror of it. A retired escrow contributes none.
- **The chain's nonce cap** (`views.go`, `ShareChainMaxNonce`) — the composition root subscribes it to every chain snapshot, and it hands `max_nonce` to each published and draining session, so the shared session stops composing active work past `types.MaxActiveNonce` and keeps the rest for finalization. The registry keeps the last value it shared and sets it on every serving session it opens afterwards: the one it publishes, and the one it rebuilds for an escrow it no longer holds to sweep and finalize it (`settlement.go`, `openServingSession`). Without it that rebuilt session would vote timeouts past the hosts' cap; every host refuses them, so finalize would start above the cap and never gather a signature quorum. Until the first value is shared it settles none, held or rebuilt: the pre-finalize sweep and finalize return `escrow.ErrDevshardBusy`, so the escrow manager retries the escrow on a later tick instead of settling it with no cap, and the routine execution-timeout sweep (`timeout_sweep.go`) skips every escrow. `ShareChainMaxNonce` stores the value and hands it to the sessions under the registry's lock, and `Add` sets it on its session under that lock just before it publishes, so an `Add` racing the first share still publishes with it. A session published at boot before the first value is shared serves without the cap until that share, behind routing's nonce ceiling ([`docs/routing.md`](../docs/routing.md)); its sweeps and settlement wait for it.
- **Open records** (`open_records.go`) — each published escrow's `Pending`, `Started` and `Challenged` records for the escrow manager's funding planner, read through an index the entry keeps.
- **Settlement handles** (`settlement.go`) — a retired escrow still resolves, because its committed nonces have no other settlement path.

## Boundaries

- **A retired escrow does not disappear.** Its nonces still owe votes, and the session that can post them outlives its routability.
- **The in-flight hold is taken with the nonce commit and refused once retired**, so an escrow cannot start work it will not be able to settle. The race of a nonce already committed still takes one on a draining escrow, since that work is owed either way. An escrow retired with `RetireAtNonceCap` also grants its `EscalationHold` to that race's escalations while it is busy and its session active (`views.go`, `escalationHoldFor`); `Retire` grants none.

## The published set and its readers

`liveSet` is the published escrow set, replaced rather than mutated, so a reader takes it with one atomic load and never waits on a writer. `newLiveSet` orders each model's candidates, and the full set `Snapshot` walks, by escrow id: the stable order the tie-break assumes, computed once when the set is published rather than sorted again on every read.

`escrowEntry.inFlight` is shared by every published set the entry appears in, so a rotation never loses the count of requests already running against it.

`Snapshot` returns every published escrow in id order plus the retired ones still draining. It takes no registry lock — only one O(1) read of each escrow's session phase, under that session's own read lock — and `publishDrainingLocked` republishes the draining view under the registry lock precisely so a scrape cannot wait on a retirement.

`RoutableSession` is the read-only handle the status routes read. It takes no in-flight count and is not the dispatch path: a race resolves its escrow through `AcquireCommitted`, which returns the session and its release together, published or draining, so a handle cannot be held without the hold. Warmup resolves through `Acquire`, which sees only the published entry. `holdFor` is the scheduler's view of the same count, taken in the step that commits a nonce; it is bound to the entry rather than to its id, and re-checks that the published entry is still that entry, so a hold cannot land on a replacement published under the same id.

`SettlementSession` resolves the handle this process still holds, published or draining — asymmetric with routing's published-only lookup. See `rules.md`, "4. Routing and settlement read the escrow set asymmetrically". `HoldSettlement` resolves the same handle with a settlement hold taken, and is what finalizing, building a settlement, inspecting and the admin finalize and signature-collection routes run under. A draining escrow closes only when neither requests nor settlement holds remain, so a forced settlement, which runs on an escrow that still has requests, is not cut off by the last request finishing mid-collection. A settlement hold is counted apart from requests: it never makes the escrow busy, never enters `ActiveUsers` and never moves routing, so a status read stays a read. Once the close is claimed, `HoldSettlement` refuses, and a caller that needs the escrow rehydrates it from storage instead.

The execution-timeout sweep takes the same kind of hold through `holdForSweep`, on published and draining escrows alike, so a race still holding a retired escrow does not keep its due timeouts from a vote: a retirement waits for its vote to end before closing the session, and `IsBusy` reports the escrow busy so a settlement or an admin delete waits for the vote. The sweep is not a request, so it never enters `ActiveUsers`: a request-counted hold would raise the escrow's load score. It is counted apart from settlement holds (`sweepHolds`), and also stays out of a `Finalize` through the per-escrow lock (`session.go`, `sessionHandle.SweepExecutionTimeouts`).

`SweepBeforeFinalize` runs the same session sweep with no race grace on an escrow about to finalize, under a settlement hold or on a serving session rehydrated for it: finalize pays every open record its full reserve, and an escrow parked across a restart has no session the tick sweep can reach. It waits for the per-escrow lock instead of stepping over a sweep or `Finalize` holding it, because a skipped sweep would let finalize pay every record the tick sweep had not reached yet; a context that ends while it waits returns the context's error (`session.go`, `sessionHandle.SweepBeforeFinalize`). The escrow manager decides how many votes it may post and whether to wait for more (escrow `README.md`, "Settlement and retirement").

`Add` publishes serving and `AddReserve` publishes a reserve, two exported entry points over one unexported `add(ctx, escrowID, model, entryFlags)`. `ReserveTaken` clears the reserve flag with a compare-and-swap, so however many picks race for a reserve the manager is told once (`registry.go`, `ReserveTaken`; see [routing.md](../docs/routing.md), "A reserve escrow").

## Reading an escrow's open records

`OpenRecords(escrowID)` returns the session config and every record still `Pending`, `Started` or `Challenged`, and `false` for an escrow not published. `LiveInferences` copies every unsealed record, about 1 ms per escrow at ten thousand of them, so a session that also offers the live id set and the applied nonce (`OpenRecordReader`, which `sessionHandle` satisfies over the state machine's existing `Config`, `LatestNonce`, `LiveInferenceIDs` and `GetInference`; every session has `Inference`, so the other three are what select the index) is read through a per-escrow index on `escrowEntry.openRecords`; any other session falls back to filtering `LiveInferences`.

The index's first read takes every live id and marks every nonce up to the largest of them and the applied nonce examined. Each later read adds the nonces applied since the last one, re-reads the ids it holds open, moves a `Finished` record to a finished list and forgets any other closed or sealed record. Only `Finished` can turn open again, when a late invalid vote challenges it, so each read also re-reads one slice of `finishedRereadsPerRead` (512) finished ids in rotation. A late `Finished`→`Challenged` flip therefore surfaces within ceil(finished/512) reads (about twenty ticks at ten thousand unsealed records), so challenged money read from the index lags; the planner counts it only as stuck. One read after the first costs about 45 µs at ten thousand unsealed records with sixty-four open (`open_records_bench_test.go`).

## Publishing, retiring and draining

`Add` publishes one escrow for routing, refuses an id an earlier entry is still draining, and releases an unpublished session without flushing.

`openingLock` serializes opens of one escrow. A session is a SQLite file: two concurrent opens fail with `SQLITE_BUSY` before either reaches the already-published check, so the escrow an operator just created can report a failure while another caller serves it. The locks are kept for the life of the process — one small mutex per escrow id ever published — because removing one a waiter still holds would let the next caller take a fresh lock and race it.

A `Listing` adds escrows from rows read after it was taken and skips any escrow retired since: `unpublish` records every retired id in each open listing under the registry lock, whether or not the escrow was live, and `add` checks that record under the same lock right before it publishes. A park, a deactivation and an inactive registration write the row inactive before they retire the escrow (the last two also retire it before the write), so a row a republish pass read active is retired after the pass took its listing; under the registry lock that retirement either comes first and the add skips the escrow, or comes after the publish and finds the entry live. Without it a pass working from a stale list reopens an escrow parked for settlement, and nothing retires it again.

`ErrDraining` refuses an escrow id whose earlier entry has not finished: that entry owns the nonces still awaiting votes and holds the storage they settle through. `ErrClosed` rejects work on a registry whose sessions are already released.

`unpublish` takes an escrow out of routing and reports whether the caller owns its close. An entry stays in `draining` until the close finishes, whether or not requests were running, so `Add` refuses the id for the whole close rather than opening a second session over storage the first still holds. For the same reason `escrowEntry.close` reports whether the store was released: `Close` runs even when the flush failed, and an entry whose store is gone must stop refusing its own id.

`closeDraining` releases the session with the registry lock free. Closing flushes the snapshot, which takes the session lock, and a dispatch takes the session lock before the registry lock: holding the two in the opposite order here would wedge every later route and settlement behind one retirement. It also keeps the snapshot write and the storage close off the path of every pick.

`claimCloseLocked` hands a retired escrow's close to exactly one caller, once neither a request nor a settlement holds it, and marks the entry `closeClaimed` under the registry lock so no hold is taken on a session already closing. A close that fails to release the store clears the mark, so the next hold's release tries again.

`DrainCloseFailures` counts the drained escrows whose flush or close failed. `Retire` and `Close` hand that error to their caller; here the request that kept the escrow open has already been answered and there is nobody left to return it to, so it is counted instead of lost.

`IsBusy` satisfies `escrow.SettlementSource`: a settlement must not claim funds while a request is still spending nonces on the escrow, including one still draining from an earlier session of it.

`HoldsSession` reports whether the registry still holds an escrow's session, published or draining, through the same lookup as `SettlementSession`. An import refuses such an escrow: a draining session held only for settlement is neither busy nor routable, yet flushes its snapshot onto the storage once the hold ends.

`Exhausted` reports an escrow routing declined as spent — out of nonces or out of deposit — to the rotation lifecycle, which is what replaces it.

`flushPendingAtRetirement` runs first inside `closeDraining`: the transactions a host gossiped back reach the state machine only when a diff carries them, and a retiring escrow has no request left to compose one, so whatever it holds would close with the session. An escrow holding nothing pending is never asked to spend a nonce on an empty diff, the send is bounded by a few seconds so an unreachable host cannot hold open the request whose release triggered the close, and a failed send is narrated rather than returned -- the close must finish or the id stays refused. See [docs/routing.md](../docs/routing.md), "The last diff of a retiring escrow".

`Deps.Retiring` is told about an escrow inside `closeDraining`: after the last request has released it and before `escrowEntry.close` releases the storage, which is the last moment anything can be read from it. Retirement is where an escrow stops being readable — the nonce ledger resolves a session only for what the registry publishes, and an operator's deactivation, a settlement and an escrow the chain no longer has all end in that close. `Retire` is the wrong place to read: a retirement waits for the requests already running, and those are exactly the inferences the reading is for. The observer runs with no registry lock held, in the session-then-registry order a dispatch already takes, and an escrow that was never published closes nothing and is never read. A successful `Finalize` tells it once more (see "Settlement and inspection"). See [docs/accounting.md](../docs/accounting.md), "Money and tokens".

`Deps.Closing` is told about every escrow `Close` still holds, live or draining, just before `escrowEntry.close` releases it, so a shutdown hands the ledger a last reading the way a retirement does, without retiring anything. See [docs/accounting.md](../docs/accounting.md), "Storage".

Each transition above — published, retired idle or draining, a drained escrow closed or failing to close — and a settlement payload that would not verify is narrated through `Deps.Narrator`, which the composition root binds to the journal. `Add` and `unpublish` call the narrator under the registry lock, so it must queue and return; a drained escrow's close and an unverifiable settlement are narrated with the lock free. A registry built without a narrator behaves the same and says nothing.

## The two session kinds

`ServingSessions` opens a chain-backed session with host clients (`user.NewHTTPSession`) — the only kind that can dispatch. `ReadOnlySessions` rehydrates from local storage alone (`user.NewLocalSession`): no chain, no host clients, so it can build a settlement but can neither serve nor finalize.

Both rebuild the session through `user.RecoverSession`, which reads the escrow's journal in pages of at most 64 nonces or 8 MiB (`storage.ReadDiffPages`) for the replay, the backfill, the applied-transaction keys and the validation-observability rebuild (`storage.RebuildValidationObsFromJournal`), and trims what it holds to the newest 256 diffs as it goes, so reopening an escrow at its nonce ceiling holds one page of its journal rather than all of it ([request.md](../docs/request.md), "A session keeps only the newest diffs in memory").

A factory handed an escrow with no record must fail with an error wrapping `escrow.ErrUnknownEscrow`. Callers tell that apart from a load failure to answer 404 rather than 502, and a factory that returns its own error for a missing escrow turns "no such escrow" into "the gateway is broken".

Three `EscrowSession` methods have non-obvious contracts. `SealedInferences` counts what sealing has drained out of `SnapshotState().Inferences`, which holds only the live tail, so a reader without it mistakes that tail for the escrow's whole history. `UserSession` is the concrete handle the dispatch boundary needs, and one rehydrated read-only has no host clients, so sending through it is a bug. `ProtocolVersion` is taken once, when the handle is built: both writers of the version, the state machine's construction option and the snapshot restore inside `user.RecoverSession`, run before `NewSessionHandle`, and a rehydrated escrow gets a new handle. Reading it on demand would cost a state copy on every status request. The two sweeps differ on the per-escrow lock: `SweepExecutionTimeouts` steps over an escrow a sweep or `Finalize` holds and `SweepBeforeFinalize` waits for it, so only the periodic sweep may skip an escrow.

## Nonces and ghost burns

`nonceStream.slots` and `nonceStream.participants` are taken once, at construction: the group is fixed for the escrow's life, and asking the session takes the lock a nonce commit holds. For the same reason `LatestNonce` reads `user.Session.PublishedNonce`, a copy of the session's nonce kept beside it under that lock and read without it, so the escrow pick of every request and every status poll does not wait out a commit writing its diff to the store. It carries one participant per slot, duplicates kept, so `nonce % GroupSize` indexes it — the group size is its length, and the escrow pick walks it to forecast burns ([routing.md](../docs/routing.md), "Pricing an escrow by the burns it will cost"). `newNonceStream` is the only way to build one, so the cached group cannot disagree with the session's. Its hosts' chat budgets (`ChatBudgets`) are taken the same way, one connection per participant, and `ChatBudgetSpent` reads one without the session's lock: the connection holds the budget, not the session ([routing.md](../docs/routing.md), "A host with no chat budget").

`errNonceDeclined` leaves the bound nonce unconsumed, so the next caller sees the same nonce.

`ghostPrompt` is the synthetic `MsgStart` a burned nonce commits: composed into the diff, never sent to a host ([routing.md](../docs/routing.md), "Ghost burns"). It has to be exactly what the record carries, because a verifier checks a vote raised for that nonce against the record's own prompt hash.

A ghost's `StartedAt` is seconds, like every other `StartedAt`: a verifier measures the refusal deadline as now-in-seconds minus this, so a millisecond stamp would keep that difference negative and the timeout on a burned nonce would be rejected every time. See `host/timeout.go`, `VerifyRefusedTimeout`.

## Membership and capacity

`slotCounts` counts the slots each participant occupies in one escrow: a validator holding several slots repeats in the per-slot key list.

`hostShares` is slots(participant, escrow)/totalSlots(participant, model) — counted over the escrows of that escrow's model only, since a host's weight is per model — the normalised per-host multiplier `limits.Capacity` expects. `pushMembershipLocked` republishes every live escrow's share rather than only the one that changed, because the denominator moves with the set.

The two notification interfaces are both called from paths that must not block. `exhaustion` is called from the request path, once per spent candidate per pick and once per reserve taken, so an implementation must mark and return rather than do I/O. `publications` is called while the registry holds its lock, so an implementation must return without doing work of its own.

## Settlement and inspection

`Finalize` and `BuildSettlement` both satisfy `escrow.SettlementSource` and both handle a non-resident escrow, but they rehydrate it differently. Finalizing collects host signatures, so it needs a serving session; a settlement payload comes entirely from local storage, so a read-only one is enough — no chain access, no host clients.

`Finalize` runs even for an escrow already in settlement, because the phase does not say whether the quorum is held. Signatures are persisted per nonce and restored on rehydration, but a finalize that fell short of quorum still leaves the escrow in settlement without them. An escrow a restart or a cancelled settle caught mid-finalize stays in finalizing, and `user.Session.Finalize` first sends its remaining finalize rounds until the phase leaves finalizing; the session handle's `Finalize` only adds the per-escrow lock, so it never races a sweep or a finalize already running, and gives up waiting for it when its context ends (`session.go`, `sessionHandle.Finalize`). `user.Session.Finalize` returns at once when the quorum is present and otherwise collects the missing signatures from the hosts; skipping it would broadcast the same unsigned payload on every retry.

After a successful `Finalize`, the registry hands the session to the `Retiring` observer again, so the ledger reads the payments finalize made to open records. The reading taken when the escrow drained still saw those records open, so without this one the ledger's charged total stays below the chain's (`observeFinalized`).

`Inspect` resolves a session for reading alone. A live or draining escrow answers from its own session; one already retired is rehydrated from local storage, which is exactly when an operator asks these questions. The returned release closes a rehydrated session and does nothing for a resident one.

The payload carries `user.Session.SettlementSignatures`: each signature checked against the gateway's own state root, spread over every slot its validator owns and copied under the session lock. Short of a checked quorum it carries every signature the session holds, copied under the same lock (`SignaturesAt`), and the shortfall is narrated as unverifiable: a read-only session has no warm-key resolver, so a signature by a warm key the state has not cached yet fails here and passes on chain. Before a payload leaves, `settlementUnverifiable` runs the chain's own check over it — recomputed state root, recovered signers, and weight against 2/3+1 — using the snapshot's group and warm keys, and against the same host stats the transaction will carry rather than the raw map, which may hold nil slots. It **warns and never blocks**: warm keys are populated lazily, so a refusal here can freeze a settlement the chain would have accepted.

## Read next

- [`docs/routing.md`](../docs/routing.md) — the escrow registry, where the nonce, the slot and the hold are taken, membership, and ghost burns.
- [`docs/capacity.md`](../docs/capacity.md) — what in-flight actually counts.
