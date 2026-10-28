# `nonces` — what feeds the ledger

[`accounting`](../accounting/) is a book of facts. This package is the only thing that writes to it.

## What it owns

- **`Recorder`** — opened by `Open` when nonce accounting is enabled, `nil` when it is not, so a disabled ledger costs nothing and every call site tolerates the nil.
- **Three live sources**, each delivered by the [`journal`](../journal/)'s consumer rather than on the producer's goroutine. The race, through `RecordRace`; the scheduler's burns, through `RecordGhost`; the timeout votes, through `RecordTimeout`.
- **Two chain sources.** A per-escrow diff watcher that hands every composed diff to the [`journal`](../journal/), which reads its validation verdicts, applied timeouts and service nonces under the session lock and applies them later through `RecordDiffFacts`; and a periodic sweep that reconciles finished nonces, host stats and open challenges with what the chain actually holds.
- **The warmup probe's settlement**, through `RecordProbe`, delivered by the journal. When the book refuses the probe, `RecordProbe` returns the refusal and the journal writes `escrow warmup could not settle its nonce`: only the book can see it.
- **The HTTP listener** that serves the book it owns as JSON, built whenever the ledger is enabled.

## What it does not own

It does not classify. Which counter a nonce lands in is `accounting`'s decision; this package only reports what happened, in the vocabulary the ledger admits.

## Boundaries

- **A disabled ledger is a nil `*Recorder`, not a no-op object.** Every method tolerates a nil receiver, which is what keeps the enabled and disabled paths from diverging.
- **The sweep is the safety net, not the primary path.** Live events are recorded as they happen; the sweep exists because the gateway can miss one — a restart, a dropped diff — and the chain is the authority.
- **A published escrow wakes the sweep.** The ledger opens an escrow on a sweep, and a fact for an escrow it has not opened is refused (`accounting.ErrUnknownEscrow`). The registry tells the recorder when it publishes an escrow (`Recorder.EscrowPublished`, beside the warmup), and that wakes the sweep at once instead of on its next interval, so the facts of a new escrow's first requests are not dropped for up to `nonceAccountingSweepInterval`. The call only marks and returns: the registry makes it under its lock. A fact that still arrives before that sweep, or after retention pruned its escrow, is lost rather than refused, so it writes no `nonce accounting refused a fact` line; `devshard_gateway_nonce_ledger_unknown_escrow_facts_total` counts it instead (`recorder.go`, `reportFact`), a warmup probe's included, which its caller also names as a refused probe, and the sweep's own reads of an escrow it has not opened stay uncounted.
- **The book's lock is never taken under a session's lock.** The diff observer runs under the lock of the session that composed the diff and only appends to the journal. The watcher installs itself only with the journal `Start` receives, which `app/lifecycle.go` passes from the composed gateway. It remembers which session it was installed on, not only which escrow, so a sweep that read an escrow just before it retired cannot leave the session it is re-published with unwatched (`sweep.go`, `watchDiffs`).
- **The first watch of a restored escrow replays what the last run dropped.** A shutdown that abandoned work leaves diffs composed after the journal closed; the sweep hands the journal the retained ones above the latest nonce the restored ledger held, once per escrow (`sweep.go`, `watchSessionDiffs`). See [`docs/accounting.md`](../docs/accounting.md), "Storage".
- **Readings never interleave.** A reading is a sweep's pass over one escrow, `EscrowClosing` or `EscrowRetiring`. Each holds the recorder's reading lock from the session snapshot to the last fact it files, so a reading from an older snapshot never lands after a newer one, and a nonce one reading folded from its sealed record is never put back as held by a sweep that snapshotted it live. A sweep resolves the escrow's creation epoch, which may ask the chain, before it takes the lock, and checks again under the lock that the escrow is still routable, because the registry unpublishes an escrow before its retirement's reading. The lock is a one-slot channel rather than a `sync.Mutex`, because a goroutine waiting on a channel counts as durably blocked in a `testing/synctest` bubble, so `reading_order_test.go` can show a reading waiting for the one in progress. Within one reading, each nonce's sealed record is looked up once (`sweep.go`, `sealedRecords`). A nonce whose sealed record says it timed out can never finish, so the recorder remembers it per escrow under the reading lock and no later reading looks it up again; it forgets an escrow's set when the escrow retires (`sweep.go`, `closeSealed`). A set for an escrow that `ResetEpoch` drops from the ledger stays until that escrow retires.
- **The lock order is recorder, then session, then book.** Under the reading lock a reading takes the session's locks (`SnapshotState`, `SealedInference`, `IsNonceFinished`) and the book's. Nothing that holds a session's or the book's lock calls a reading: the registry calls `EscrowRetiring` and `EscrowClosing` with its own lock free and after the session call that preceded them returned, `RoutableSession` and `Snapshot` take no lock, and the diff observer that runs under the session lock only appends to the journal.
- **`Stop` ends the sweep and waits out the one in progress.** The shutdown runs it as the `nonce sweep` step, before the escrow sessions close (`app/lifecycle.go`, `shutdownOrder`), so no sweep reads a closed session, and none runs after `Registry.Close` emptied the live set, which would retire every escrow the ledger holds.
- **`Start` serves the listener and sweeps; `StartSweeping` only sweeps.** The scenario harness uses `StartSweeping`, because a listener blocks in `Accept` outside a synctest bubble's control.

## The judgements it does make

Not classification — that is `accounting`'s — but the four readings of a raw fact that have to happen before the fact can be reported at all:

- **The epoch a swept escrow is stamped with is the one the chain stamped on it at creation**, resolved per escrow and memoised, because an escrow's creation epoch never moves (`CreationEpochFunc`, `Recorder.epochOf`). An escrow whose epoch cannot be resolved is not opened at all, so no fact is filed under a guess: a guess is indistinguishable from a fact to everything downstream, and `Book.OpenEscrow` pins the first value it is given for the escrow's life.

  First sighting cannot stand in for creation. The counters are restored from `accounting.db`, so "first seen" is a property of that file rather than of the escrow, and that file is dropped and rebuilt whenever `accounting.SchemaVersion` moves, silently and without an error; it is emptied for one epoch by `POST /v1/admin/accounting/reset/{epoch}`, which deletes live escrows and not only retired ones, and pruned by retention. Meanwhile the chain-side half of every escrow — its latest nonce, its lifetime `HostStats`, its chain cost — is re-read whole on every sweep whatever the ledger remembers. A re-sighted escrow would therefore file its entire history under whatever epoch the gateway happened to be running in.

  The resolver reads the stored chain epoch, else the chain's answer, else the row's label: the row's `devshards.chain_epoch`, the copy of the chain's `DevshardEscrow.epoch_index` the escrow manager resolved, needs no chain call; an unresolved row asks the chain, whose `epoch_index` is the authority; and only when the chain cannot answer does it fall back to the epoch this gateway recorded when it created the escrow (`devshards.rotation_epoch`). Both columns live in a different database that a schema bump does not touch. A refusal is never memoised, so an escrow the chain could not be reached for is stamped on a later sweep rather than never.

  One consequence is worth knowing before using it: resetting an epoch whose escrows are still live now re-populates it on the next sweep, because they are still that epoch's escrows. The route clears what an epoch holds; it does not move an escrow out of the epoch it belongs to.
- **A slow receipt is measured from the dispatch, and only where both stamps exist.** An attempt that never got a receipt is a refusal, which the ledger already counts; calling it slow as well would report one failure twice.
- **Clock drift counts in either direction.** A host running ahead makes the gateway wait past a deadline that already passed; one running behind makes it vote on a nonce still being served.
- **A winner crowned after its client left gets its own terminal.** It still counts as work the host delivered, but it is not an answer anybody read — and without a name of its own, the population where the race outlived its client cannot be found in the ledger at all.

## Read next

- [`accounting/README.md`](../accounting/README.md) — the book this fills.
