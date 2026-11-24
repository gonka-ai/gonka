# What a settlement would record, before settling

A settlement submits `MsgSettleDevshardEscrow`, and the part of it that concerns a host is one `host_stats` entry per slot: `slot_id`, `missed`, `invalid`, `cost`, `required_validations`, `completed_validations`, `validated`, `finished`, and (v7+) `assigned`.

Both gateway binaries expose participant accounting at these endpoints.

```
GET /api/v1/epochs/{epoch}/participants
GET /api/v1/epochs/{epoch}/participants/{participant}
```

## Where each number comes from

| Settlement field | `devshardctl` participant record | `cmd/gateway` participant record |
|---|---|---|
| `missed` | `protocol_misses` | `protocol_misses` |
| `invalid` | `protocol_invalid` | `protocol_invalid` |
| `required_validations` | `required_validations` | `required_validations` |
| `completed_validations` | `completed_validations` | `completed_validations` |
| `validated` | `protocol_validated` | — |
| `finished` | `protocol_finished` | — |
| `cost` | — | `chain_cost` |
| `assigned` (v7+) | — | — |

The new gateway exposes the raw per-slot host statistics through its admin endpoint `GET /devshard/{id}/v1/debug/state`. Its participant field `assigned_nonces` measures the slot's share of all nonces. The settlement field `assigned` counts accepted inference starts.

The state machine records a miss when a timeout applies to the executor slot and an invalid inference when a validator's verdict applies. A successful sampled replay increases `validated`, a challenge can decrease it, and a rejected challenge restores it. The host's reward for serving inferences accumulates in `cost`.

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

The signed v7 count is also available in snapshots and settlement JSON. [v7 Assigned accounting](./v7-assigned-accounting.md)
