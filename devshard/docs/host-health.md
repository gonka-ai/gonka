# Host Health: Quarantine and Performance Tracking

The devshard proxy uses two independent systems to handle misbehaving hosts:

1. **ParticipantRequestLimiter** — automatic probe/shadow quarantine and recovery
2. **PerfTracker** — soft performance signal, influences speculative decisions

Both are keyed by participant identity (gonka validator address, bech32).
Automatic quarantine can also be scoped to one or more model IDs, so a host can
be suppressed for the model it misbehaved on without suppressing the same
participant for unrelated models.

## ParticipantRequestLimiter (Quarantine)

`ParticipantRequestLimiter` tracks temporary health state for participants. A
single per-model `failure_strikes` counter drives soft failures and probation:

- `0` strikes: healthy, can win.
- `1` or `2` strikes: probation. Real traffic is sent, but the host is
  no-winner for the affected model.
- `>= 3` strikes: quarantine.

There are two automatic quarantine modes:

- **Probe quarantine**: no real inference traffic is sent to the host for the
  affected model. Nonces that land on the host are burned as silent ghost probes
  (the `MsgStartInference` is composed locally but no HTTP call is made).
- **Shadow quarantine**: real inference traffic is still sent for the affected
  model, but the host is marked no-winner. If it produces content, redundancy
  escalates so another host can win, and bad attempts still trigger timeout
  votes/accounting.

The manual suspicious-host endpoint uses the same no-winner behavior as shadow
quarantine, but it is permanent until removed through the endpoint. Automatic
quarantine is temporary and expires by wall-clock time.

### Quarantine triggers

| Trigger | Mode | Duration | Scope | Details |
| --- | --- | ---: | --- | --- |
| HTTP 429 or 503 | Probe | 60 min | Request model | Host reported overload or rate limit |
| HTTP 404/403 on inference | Probe | 30 min | Request model | Escrow not registered on host, forbidden, or otherwise unavailable for inference |
| Timestamp drift HTTP 401 on inference | Probe | 30 min | Request model | Host clock drift exceeds allowed bound |
| Non-EOF transport failure on inference | Probe | 30 min | Request model | Dial timeout, connection refused, TLS error |
| 3 soft strikes from EOF transport failures and/or empty streams | Probe or Shadow | 30 min | Request model | EOF-style transport failures use probe quarantine; empty-stream failures use shadow quarantine. Mixed soft failures share the same counter. |
| Transport failure on non-inference | None | none | none | Logged but not quarantined. A flaky vote RPC should not remove an otherwise healthy inference host. |
| Empty stream soft strike | Probation, then Shadow at 3 strikes | 30 min at quarantine | Request model | Host returns receipt but zero content chunks. Empty streams are only counted when the overall request succeeded via another attempt. |
| EOF transport soft strike | Probation, then Probe at 3 strikes | 30 min at quarantine | Request model | EOF-style stream/read failure on inference. |
| Stalled winner | Shadow | 30 min | Request model | Host won the race, emitted content, then went silent long enough to trigger the inter-chunk stall timeout. Immediate quarantine, no streak. |
| Manual suspicious host | Shadow/no-winner | no expiration | Participant | Controlled by admin endpoint, persisted separately, cleared only by endpoint removal. |

### Quarantine behavior

- Probe quarantine drains the participant token bucket to zero for the affected
  model. `AllowRequest` rejects host calls for that model, and the picker burns
  ghost probes instead.
- Shadow quarantine keeps the host callable for the affected model, but
  `Redundancy` marks attempts as no-winner. This is the same winning behavior as
  suspicious hosts, but with a temporary expiry.
- The longer of overlapping quarantines wins (for example, a 503 during a
  30-minute transport quarantine extends it to 60 minutes).
- Automatic quarantine state is persisted to `gateway.db` in
  `participant_throttle_state` and survives container restarts. Persisted state
  includes `model_ids` and `failure_strikes`; an empty model list means
  legacy/global quarantine.
- `quarantine_until_utc` is wall-clock based. If the gateway is down past the
  expiry, the host does not restart the full timer on boot.
- When automatic quarantine expires, the participant enters probation with
  `failure_strikes = 2`. Probation is shadow/no-winner: traffic is still sent,
  but the host cannot win.
- A successful inference for the affected model decrements `failure_strikes` by
  one. At `0`, probation succeeds, the host is removed from tracking, and its
  persistent automatic-quarantine row is deleted.
- A soft failure during probation increments the same counter. From the
  post-quarantine `2` state, one more soft failure reaches `3` and re-enters
  quarantine.

### Admin override

```http
POST /v1/admin/participants/unquarantine
Content-Type: application/json
Authorization: Bearer $DEVSHARD_ADMIN_API_KEY

{"participant_key": "gonka1abc...xyz"}
```

Immediately clears quarantine and resets the token bucket. The host becomes
available for the next nonce that maps to it. This endpoint clears the
participant's automatic quarantine row; it is not currently model-specific.

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
| --- | --- |
| `Responsive` | `true` if `resp.ConfirmedAt > 0` AND not an empty stream |
| `SendTime` | Wall clock when `SendOnly` was called |
| `ReceiptTime` | Wall clock when `devshard_receipt` SSE event arrived |
| `FirstToken` | Wall clock when first content chunk arrived |
| `TotalTime` | Wall clock from send to stream completion |

### How it influences decisions

`Redundancy.Decide(hostIdx, inputLength)` checks PerfTracker before each
primary dispatch:

| Decision | Condition | Effect |
| --- | --- | --- |
| `primary_unresponsive` | `PerfTracker.IsUnresponsive(hostIdx)` — `ResponsiveRate < 0.5` in the rolling window | Start secondary immediately (delay=0) |
| `secondary_faster` | Secondary host's estimated time is ≥50% faster than primary's | Start secondary immediately (delay=0) |
| `receipt_timeout` | Default — neither of the above | Start secondary after `ReceiptTimeout` (5s) if no receipt arrives |

### Key differences from quarantine

| Property | ParticipantRequestLimiter | PerfTracker |
| --- | --- | --- |
| Blocks traffic? | Probe quarantine: yes. Shadow quarantine/probation: no, but no-winner. | No — host still gets real requests |
| Keyed by | Participant (gonka address), optionally scoped by model ID | Host index (slot position) |
| Scope | Inference-path failures for the affected model; manual suspicious is participant-wide | Inference only |
| Persisted | Yes (gateway.db) | Yes (perf store), but rolling window |
| Cross-escrow | Yes (process-wide) | No (per-escrow runtime) |
| Recovery | Time-based (30-60 min), two post-quarantine success decrements, or admin override | Automatic — good samples push out bad ones |

## Interaction between the two systems

The two systems are independent and can overlap:

- A host can be perf-tracked as unresponsive (triggering immediate secondary
  dispatch) without being quarantined (it still receives real traffic).
- A probe-quarantined host is invisible to PerfTracker for the affected model
  because no inference attempt is made.
- A shadow-quarantined or probationary host can still produce PerfTracker
  samples because it receives real attempts, but it cannot become the winner.
- When probe quarantine ends, PerfTracker may have no recent samples for the
  host, so `IsUnresponsive` returns false (no data = not unresponsive), and the
  host re-enters the normal `receipt_timeout` decision path while probation
  keeps it no-winner.
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
- `participant_limit_rejected` — request blocked by probe quarantine

### Performance

- `stage=decision_made decision=primary_unresponsive` — perf-based immediate secondary
- `stage=decision_made decision=secondary_faster` — perf-based immediate secondary
- `stage=decision_made decision=receipt_timeout` — default, wait for receipt
- `stage=receipt_timeout_wait_elapsed` — receipt didn't arrive in time, secondary started
