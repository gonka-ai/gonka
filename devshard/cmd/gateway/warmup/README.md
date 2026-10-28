# `warmup` — teaching a new escrow to its own group

An escrow is published to the chain before its hosts know it exists. Without a probe, the first real request pays that discovery one host at a time.

## What it owns

- **`Prober`** — on publication, spends one nonce on a probe so every host in the group learns the escrow and catches up on its diff chain. Built only when `warm_new_escrows` is on, and `nil` otherwise.
- **The settlement of that nonce** (`settle.go`). The probe's nonce is committed outside the scheduler, so nothing else would ever settle it: the escrow would pay the reserve and the host would escape the miss. A refused probe votes its own timeout. The warmup's twenty-minute budget bounds only the wait for the group, the seed and the probe; the vote waits for its own deadline, which for an execution timeout lies past the budget, and ends only with that deadline or the gateway's shutdown. The escrow stays held for as long as the vote is owed, as a race's does. Whatever the probe's answer, the warmup then arms the session's quiet settle, as a released dispatch does: a fresh escrow may take no request for minutes and a reserve none at all, and without it the probe's receipt and Finish wait for retirement while the hosts hold the inference open.

## What it does not own

It is not a health check and not a scheduler. It runs once per escrow, on publication.

## Boundaries

- **The warmup writes no line.** Its transitions — no nonce to spend, warmed, the ledger refusing the escrow, its own vote — are narrated through `SetNarrator`, bound to the journal by `app/routing.go` beside `Serve` in `newRouting`. The journal omits a nil error and writes a failed vote at Warn. A probe the book refuses is written by the journal (`renderProbeRefused`) from the error `nonces.Recorder.RecordProbe` returns.
- **Both dependencies bind after construction.** The registry exists only after the warmup it publishes to, and the vote path only after the sessions the race shares with it — hence `Serve` and `Settle` rather than constructor arguments. `Settle` also binds `Probes`, the journal through which the probe's nonce reaches the ledger.
- **The probe's nonce is the gateway's own work, not a user's.** The ledger records it under its own terminal so it lands on neither side of a serving ratio.
- **The probe's timeout kind is read from the receipt, the way the engine reads it.** A host that receipts and then hangs outlives `probeTimeout`, so the send errors and its reply never arrives; the receipt is therefore taken as it arrives. A kind fixed at `refused` files the expensive failure as the cheap one.
- **The escrow is opened in the ledger here, not left to the sweep.** The sweep opens escrows on its own schedule, which has not necessarily run yet — and this nonce is spent on a just-published escrow. An attempt the ledger refuses would lose the terminal that keeps the gateway's own nonce out of the host's record. The open is a direct write, so it lands before the probe, which reaches the ledger through the journal.
- **It opens nothing before the chain has named an epoch.** The snapshot carries `EpochIndex` 0 until the observer's first successful read, and `Book.OpenEscrow` pins the first epoch it is given for the escrow's life — so an escrow opened at 0 is invisible to every epoch-scoped query and cannot be cleared, because the admin reset route refuses epoch 0. A probe that skips the open loses its own terminal for that one nonce, which is the smaller loss. The escrow is opened by the sweep once the chain answers ([`nonces/README.md`](../nonces/README.md), "The judgements it does make").
- **`EscrowPublished` is announced under the registry's lock**, so it must return without doing work.

## Read next

- [`docs/escrows.md`](../docs/escrows.md) — where publication sits in the escrow's life.
