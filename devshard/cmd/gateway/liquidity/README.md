# `liquidity` — one escrow's money, by what it is waiting for

Pure: no I/O, no clock of its own, no state between calls. `Classify` reads one escrow's balance, its open records (`Pending`, `Started`, `Challenged`) and the prices it is measured against, and returns its money split four ways with three flags. The escrow manager's funding planner calls it once per live escrow per tick; nothing else does.

## Classes

| Class | What it holds |
| --- | --- |
| `free` | the session balance, already net of every reservation and fee |
| `returning` | for each `Pending` or `Started` record younger than five minutes (`returningSoon`), the estimated surplus of its input: `(InputLength − InputLength / BytesPerToken) × TokenPrice`, never more than the record's reservation |
| `late` | a `Pending` record past five minutes but before its start plus the refusal timeout, `user.TimeoutBuffer` and the race's refused-vote ladder (`refusedVoteLadder`, 450 s); a `Started` record past five minutes but before its anchor plus the execution timeout, `user.TimeoutBuffer` and the sweep grace — its whole reservation |
| `stuck` | a `Pending` record past the ladder and a `Started` record past its deadline (their whole reservation), and every `Challenged` record's `ActualCost` |

Every window counts from `StartedAt`, which the gateway sets. A `Started` record's anchor is its `ConfirmedAt` clamped to at most its start plus the refusal window, or its start when the executor stamped nothing (`ExecutionAnchor`, the same function the hold's view in `registry/views.go` uses), so an executor clock running ahead cannot stretch a deadline.

`returning` is an estimate, not a bound. The real surplus is the reservation minus `(input tokens + output tokens) × price`, and code, JSON and CJK input run more tokens per byte than one in four. The planner uses it only in its capacity term; its guarantee reads `free` alone, so a surplus that does not come back cannot fool it. The output side's surplus (`max_tokens` not used) is not counted at all.

A young record is `returning` even when its race has finished: the drain that sequences its `Finish` runs on the heartbeat's cadence, so the tick right after a race still sees the reservation. That is the money this class exists for.

## Flags

- **Full** — `free ≥ Slot`, where `Slot` is the attempts a race funds times one full-context request at the escrow's own price. `free` is the session's balance, already net of every reservation in flight, so the requests on the escrow are not counted again. Never full while unpriced.
- **Starved** — not full, and the nonce cap not reached. A nonce-capped escrow is neither: it is leaving routing.
- **Idle** — no `returning` and no `late` record. `stuck` money, `Challenged` included, does not keep an escrow busy: nothing schedules the votes that would resolve it.

## Constants

- `returningSoon` (5 min): long enough to cover the drain cadence (24 s) and a race's receipt and execution for an ordinary answer, short enough that a stalled record stops counting as liquidity within one demand window.
- `refusedVoteLadder` (450 s): the race's own refused-vote retries, 30 + 60 + 120 + 240 s (`engine/engine.go`, `refusedVoteFirstRetryDelay` and `refusedVoteRetries`). A change there must change this.

They become configuration only if production metrics show a need.
