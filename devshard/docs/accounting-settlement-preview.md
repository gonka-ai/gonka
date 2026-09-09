# What a settlement would record, before settling

A settlement submits `MsgSettleDevshardEscrow`, and the part of it that concerns a host is one `host_stats` entry per slot: `slot_id`, `missed`, `invalid`, `cost`, `required_validations`, `completed_validations`.

Every one of those numbers except `cost` is already visible while the escrow is live, per participant and per slot, at

```
GET /api/v1/epochs/{epoch}/participants
GET /api/v1/epochs/{epoch}/participants/{participant}
```

So the question "what happens if we enable settlement" does not need settlement enabled to answer.

## Where each number comes from

| settlement field | field in the record | source |
|---|---|---|
| `missed` | `protocol_misses` | the escrow's own host statistics |
| `invalid` | `protocol_invalid` | the escrow's own host statistics |
| `required_validations` | `required_validations` | the escrow's own host statistics |
| `completed_validations` | `completed_validations` | the escrow's own host statistics |
| `cost` | — | tracked internally, deliberately not published |

These are not the gateway's opinion. `missed` moves when an applied timeout lands on the executor slot, `invalid` when a validator's verdict does; both are read back from the state machine on every committed diff that carries one, and re-read in full on each phase change. They are the same numbers the settlement transaction will carry.

`cost` is the host's reward for the inferences it served. It is accumulated in the state machine but not surfaced here: the question this view answers is who a settlement would penalise, not what it would pay.

## Reading it against our own count

The record also carries what this gateway believes, so the two can be compared before either is final:

- `dispositions` — what became of each nonce the gateway sent, by outcome.
- `timeout_outcomes` — how many timeouts were applied, skipped or failed.
- `cross_checks` — the two sides side by side: applied timeouts against chain misses, recorded invalid against chain invalid, and `error_count` as their total disagreement.

Drift between the two is normal while an escrow is live and converges on its own; the `ledger_disagrees_with_chain` finding exists to say when it has stopped converging. See [accounting-findings.md](./accounting-findings.md).

## What predicts a future penalty

`invalid` is a verdict already reached. Two findings anticipate one that has not:

- `logprobs_not_token_ids` — the host answered with logprob tokens named by text instead of by id. A validator replays from those ids and cannot replay text, so it votes the inference invalid. Expect `protocol_invalid` to follow.
- `clock_drift` — the host's signed receipt stamps a deadline the chain measures from, so a drifted clock can get a nonce voted timed out while the answer is still being written. Expect `protocol_misses` to follow.

Both are visible per host long before a settlement records the consequence.
