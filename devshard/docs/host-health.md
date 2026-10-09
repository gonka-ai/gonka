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

## perf.db: what startup reads and what is pruned

`perf.db` holds `perf_host_samples`, `perf_request_log` and the `request_accounting` tables. Startup reads only the host samples of the last `2 × ParticipantPerfWindow + 1h` and the newest `requestLogSize` request log rows; nothing else reads the perf tables.

`PerfStore.LoadSamples` (`perfstore.go`) walks the host samples newest first and stops at the first live sample (empty `source_escrow`) that started more than an hour before the window. A sample is written when its request finishes but carries the time it started, so ids follow completion while `send_time` follows start; the hour of slack keeps a long request finishing late from ending the walk before samples still inside the window. Legacy samples backfilled by `BackfillLegacyEscrowSamples` carry old times at high ids, which is why only live samples end the walk. The table has no index on `send_time`, and building one on a long history would cost a restart as long as the full scan it replaces.

`perfPruner` (`perf_prune.go`), started by `Gateway.startPerfPruner` and stopped by `Gateway.Close`, deletes what nothing reads: host samples at or below the sample that ends the startup walk (every one of them finished before that sample started, more than an hour before the window, given an execution timeout well under an hour), and request log rows older than the newest `requestLogSize`. Request accounting is pruned only while accounting is on and `DEVSHARD_STATS_RETENTION_EPOCHS` is above zero (it defaults to 2; `0` keeps everything): rows go when their escrow is neither in the accounting ledger, which applies that retention itself, nor resident in the gateway. That walk reads every accounting row, so it runs once per epoch. The first pass runs a minute after start and then every ten minutes. Everything it reads or deletes, the boundary search included, goes 2000 rows at a time with a 20 ms pause between batches, because request-path inserts share the store's single connection. The retention check runs only once a batch's cursor is closed: gateway paths that hold the gateway lock also need that connection (an admin import, a legacy escrow's sample backfill), so checking residency under the lock while the cursor holds the connection would deadlock the gateway.

Deleted pages are reused by SQLite but not returned to the filesystem: pruning stops `perf.db` from growing, and shrinking an existing file takes a manual `VACUUM` while the gateway is stopped.

`NewPerfStore` (`perfstore_factory.go`) picks the backend from `DEVSHARD_STORAGE_MODE` the way `NewGatewayStore` does. `sqlite` keeps the local `perf.db` and ignores `PGHOST`. `hybrid` and `postgres` require `PGHOST` and fail to start when Postgres is down; the tables then live in the gateway store's Postgres database. Each replica imports its own local `perf.db` on its first start against Postgres, once: the import is keyed by an id kept inside that file (`perf_sqlite_import:<id>` in the gateway store's `gateway_migration` table, committed with the rows). Imports run one at a time under an advisory lock, so replicas starting together wait for each other instead of failing. Request accounting is imported from every replica, and rows Postgres already holds keep the stored version. Host samples (those above the startup walk's boundary) and the request log rows startup reads are imported only into empty tables, by the first replica: both are ordered by id, so a later replica's older history placed above live rows would end the startup walk early and let the pruner delete the serving replicas' recent samples. The whole import is one transaction bounded by `PG_IMPORT_TIMEOUT`; a timeout rolls it back and the gateway does not start, so a very large `perf.db` on a slow link needs a larger value. On Postgres the pruner deletes host samples and request log rows the same way, but never request accounting: each replica's ledger only knows the escrows it loaded at start or registered since, so one replica would delete the accounting of an escrow that lives on another. Request accounting on Postgres therefore grows until a shared retention rule exists.

Measured with the startup simulation ([gateway-load-simulation.md](./gateway-load-simulation.md), "Startup simulation") on 1 million requests (2.2 GB, 3 million host samples), cold cache, Docker Desktop on Apple silicon:

| Read limit | `NewPerfTracker` before | after |
| --- | --- | --- |
| 3000 IOPS | 40.3 s | 0.25 s |
| 1000 IOPS | 121 s | 1.0 s |

The first prune pass of that file at 1000 IOPS took 2 min 20 s and deleted 3 million host samples, 996 thousand request log rows and 4 million accounting rows, while a writer inserting every 10 ms saw p50 90 µs, p99 54 ms and max 82 ms.

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
