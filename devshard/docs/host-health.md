# Host Health: Quarantine and Performance Tracking

The devshard proxy uses two independent systems to handle misbehaving hosts:

1. **ParticipantRequestLimiter** — hard quarantine, blocks all traffic
2. **PerfTracker** — soft performance signal, influences speculative decisions

Both are keyed by participant identity (gonka validator address, bech32).

## ParticipantRequestLimiter (Quarantine)

A quarantined host receives zero real inference traffic. Nonces that land on
a quarantined host are burned as silent ghost probes (the MsgStartInference
is composed locally but no HTTP call is made). The host stays in the nonce
rotation but is effectively skipped.

### Quarantine triggers

| Trigger | Duration | Path | Details |
|---|---|---|---|
| HTTP 429 or 503 | 60 min | any | Host reported overload or rate limit |
| HTTP 404 on inference | 30 min | `/chat/completions` | Escrow not registered on host |
| Non-EOF transport failure on inference | 30 min | `/chat/completions` | Dial timeout, connection refused, TLS error |
| 3 consecutive EOF transport failures on inference | 30 min | `/chat/completions` | EOF-style stream/read failures. Streak resets on quarantine or on a successful inference. |
| Transport failure on non-inference | none | `/verify-timeout`, `/gossip/*`, etc. | Logged but not quarantined — a flaky vote RPC should not remove an otherwise healthy inference host |
| 3 consecutive empty streams | 30 min | inference result | Host returns receipt but zero content chunks, three times in a row. Empty streams are only counted when the overall request succeeded via another attempt. Streak resets on quarantine or on a successful inference. |
| Stalled winner | 30 min | inference result | Host won the race, emitted content, then went silent long enough to trigger the inter-chunk stall timeout (1 min). Immediate quarantine, no streak. |

### Quarantine behavior

- Tokens are drained to zero on activation.
- The longer of overlapping quarantines wins (e.g., a 503 during transport quarantine extends to 60 min).
- State is persisted to `gateway.db` and survives container restarts.
- When quarantine expires and tokens recover to full burst, the host is removed from tracking entirely (persistent record deleted).
- A successful inference (`ObserveSuccessfulInference`) clears empty-stream and EOF streaks but does not end an active quarantine early.

### Admin override

```
POST /v1/admin/participants/unquarantine
Content-Type: application/json
Authorization: Bearer $DEVSHARD_ADMIN_API_KEY

{"participant_key": "gonka1abc...xyz"}
```

Immediately clears quarantine and resets the token bucket. The host becomes
available for the next nonce that maps to it.

## PerfTracker (Performance Tracking)

PerfTracker records per-host inference performance in a rolling window. It
does **not** block traffic — it only influences the speculative redundancy
decision (whether to start a secondary attempt immediately vs. waiting for
receipt timeout).

### Scope

PerfTracker only observes inference attempts. Timeout voting, gossip,
challenge-receipt, and other protocol RPCs are invisible to it.

### What is recorded

For each non-probe inference attempt that reaches `race_completed`:

| Field | Source |
|---|---|
| `Responsive` | `true` if `resp.ConfirmedAt > 0` AND not an empty stream |
| `SendTime` | Wall clock when `SendOnly` was called |
| `ReceiptTime` | Wall clock when `devshard_receipt` SSE event arrived |
| `FirstToken` | Wall clock when first content chunk arrived |
| `TotalTime` | Wall clock from send to stream completion |

### How it influences decisions

`Redundancy.Decide(hostIdx, inputLength)` checks PerfTracker before each
primary dispatch:

| Decision | Condition | Effect |
|---|---|---|
| `primary_unresponsive` | `PerfTracker.IsUnresponsive(hostIdx)` — `ResponsiveRate < 0.5` in the rolling window | Start secondary immediately (delay=0) |
| `secondary_faster` | Secondary host's estimated time is ≥50% faster than primary's | Start secondary immediately (delay=0) |
| `receipt_timeout` | Default — neither of the above | Start secondary after `ReceiptTimeout` (5s) if no receipt arrives |

### Key differences from quarantine

| Property | ParticipantRequestLimiter | PerfTracker |
|---|---|---|
| Blocks traffic? | Yes — ghost probe only | No — host still gets real requests |
| Keyed by | Participant (gonka address) | Host index (slot position) |
| Scope | All paths (inference, voting, gossip) | Inference only |
| Persisted | Yes (gateway.db) | Yes (perf store), but rolling window |
| Cross-escrow | Yes (process-wide) | No (per-escrow runtime) |
| Recovery | Time-based (30-60 min) or admin override | Automatic — good samples push out bad ones |

## Interaction between the two systems

The two systems are independent and can overlap:

- A host can be perf-tracked as unresponsive (triggering immediate secondary
  dispatch) without being quarantined (it still receives real traffic).
- A quarantined host is invisible to PerfTracker because no inference attempt
  is made — no sample is recorded.
- When quarantine ends, PerfTracker has no recent samples for the host, so
  `IsUnresponsive` returns false (no data = not unresponsive), and the host
  re-enters the normal `receipt_timeout` decision path.
- A host that accumulates bad perf samples but never hits a quarantine trigger
  (e.g., consistently slow but always finishes) will stay in the
  `primary_unresponsive` or `secondary_faster` decision bucket — speculative
  redundancy routes around it, but it still processes inferences and earns
  protocol rewards.

## Diagnostic signals in logs

### Quarantine

- `participant_limit_activated` — 429/503 quarantine
- `participant_limit_transport_failure` — non-EOF inference transport failure quarantine
- `participant_limit_eof_transport_streak` — EOF inference transport failure streak increment
- `participant_limit_eof_transport_quarantine` — 3-strike EOF inference transport failure quarantine
- `participant_transport_failure_ignored` — non-inference transport failure (no quarantine)
- `participant_limit_empty_stream_quarantine` — 3-strike empty stream on requests that succeeded via another attempt
- `participant_limit_stalled_winner_quarantine` — stalled winner
- `participant_quarantine_cleared` — admin override via unquarantine endpoint
- `participant_quarantine_ended` — natural expiry
- `participant_limit_rejected` — request blocked by quarantine

### Performance

- `stage=decision_made decision=primary_unresponsive` — perf-based immediate secondary
- `stage=decision_made decision=secondary_faster` — perf-based immediate secondary
- `stage=decision_made decision=receipt_timeout` — default, wait for receipt
- `stage=receipt_timeout_wait_elapsed` — receipt didn't arrive in time, secondary started
