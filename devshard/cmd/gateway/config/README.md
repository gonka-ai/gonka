# `config` — the immutable snapshot

Everything the gateway's behaviour depends on, in one value that is never mutated.

## What it owns

- **`Build`** — merges three layers in a fixed order: defaults, then environment, then the operator's stored overrides. It clones every map and slice it takes from its inputs, so nothing it returns shares memory with its sources.
- **`Defaults`** — the single source of every default value.
- **`Validate`** — fail-fast checks at startup; an unsafe combination is refused rather than discovered later.
- **`Holder`** — an atomic holder that publishes a whole replacement snapshot and notifies subscribers. Readers take a pointer and are never torn.

## Boundaries

- **A snapshot is never mutated after `Build`.** Reconfiguration swaps the whole thing.
- **Defaults live here, never in [`env`](../env/).** A `nil` from the environment means "unset", which is not the same as a zero.
- **Zero is meaningful for several limits** — usually "unlimited". That is documented per field, because a zero that silently means something else is how a configuration lies.

## What each group holds

| Group | What it configures, and what a zero means |
| --- | --- |
| `Server` | the listener itself. `StorageDir` is resolved by `main` *before* `Build`, so its `~/.cache/gonka-gateway` default lives there, not in `Defaults`. `SessionRetentionEpochs` is how many epochs before the effective one an escrow's session directory under it is kept: 2 by default, `0` keeps every one, and `1` is raised to 2, the hosts' horizon. |
| `Chain` | `GRPCEndpoint` is what the escrow bridge dials. `common/chain` derives the CometBFT RPC host from it at the standard port, and that derived endpoint is the query fallback every escrow read inherits — a deployment that moved the RPC port must set `RPCEndpoint` explicitly. `VersionsPollIntervalSeconds` (default 15, 1 to 86 400) is how often each candidate host's `/v1/versions` is polled ([`chain/README.md`](../chain/README.md)). |
| `Tx` | fee, gas and the poll loop that waits for a transaction. |
| `Limits` | admission tuning. A zero `MaxInputTokensInFlight` is unlimited. `MaxTokensCap` bounds what a client may *ask for* and does not clamp `DefaultMaxTokens`. `ModelAccess` maps a model to one of the tiers below. |
| `Limits.Concurrency` | a zero `MaxRequests` is no static cap at all, leaving admission to the capacity-scaled per-weight limit alone. |
| `Limits.HostWindows` | the per-host congestion windows, counted in requests of the model: a floor and a starting size for input and for output, and no ceiling — where a window stops is what the host's own congestion signals say. See [`docs/capacity.md`](../docs/capacity.md), "The participant limiter: IOCW". |
| `Limits.Congestion` | the factors a narrowing multiplies the blamed window by, the factor the other window takes with it, and how far above the best latency it has held a host may drift before a healthy answer counts as congestion. |
| `Limits.ModelLimits` | the per-model override set. The two token fields are required as a pair; the pointer fields are optional, and a `nil` inherits the global limit rather than meaning zero. |
| `Modes` | PoC mode and the disabled/redirect switches. |
| `Rotation` | escrow rotation, its settlement switches (any epoch and previous epoch only), and how far before PoC it runs; `SettleMarginBlocks` (default 600, at least 1) is how many blocks before an escrow's settlement deadline it is parked for settling ([`docs/escrows.md`](../docs/escrows.md)). |
| `HeightSync` | whether the heartbeat cadence runs, whether a session chases a seeded tip, the optional mainnet follower, the anchor cadence `AnchorK` / `AnchorSlots` ([`heights/README.md`](../heights/README.md)), and `QuietSettleDelayMS`, how long a session keeps a leftover receipt or Finish for the next request before composing them on its own: 5 minutes by default, the shared session's `user.DefaultQuietSettleDelay`. |
| `HostPing` | the reachability and clock-drift probe of the live escrows' hosts: interval and timeout in milliseconds, concurrency, and a switch to turn it off ([`hostping/README.md`](../hostping/README.md)). |
| `Cache` | the response cache's byte ceiling. |
| `Accounting` | the request records, bounded on both axes; neither bound may be zero. |
| `NonceAccounting` | the per-nonce ledger, which answers a different question than `Accounting`: where every committed nonce went, rather than what became of one client request. It reaches an operator only through its own JSON API, listening on `Port` whenever the ledger is enabled. `RetentionEpochs` counts epochs back from the current one, defaults to 2, and must not be negative: 0 keeps every retired escrow for the life of the process. |
| `Capture` | the request-capture sink. An empty `Dir` means `<storageDir>/captured-requests`, `SampleRate` runs from 1 (every matching request) to 0 (none), and `MaxBytes` ceilings what the directory may hold. |
| `Stream` | the drain timeout and the three-tier classification byte budget. |
| `Perf` | Envoy-style host ejection: consecutive-fail and rate-with-min-volume triggers, timed backoff, and the pool-wide ejection cap. `MinAvailableHosts` is a floor kept routable regardless of that cap, and host-model state unseen for `HostStalenessSeconds` is evicted. |
| `Engine` | race-escalation tuning. A zero `MaxAttemptsPerRequest` is bounded only by the host group, and the backstops this struct does not carry are engine constants — see [`docs/race.md`](../docs/race.md), "Tunables and backstops". |
| `Scheduler` | `MatchWaitMS` is how long a bound nonce waits for a co-arriving compatible request before it is burned; 0 burns immediately. `MaxConsecutiveBurns` is how many nonces one escrow may burn in a row before the next binding is sent over a full congestion window instead; 0 turns that off. `DispatchQueueDepth` is how many waiters one escrow's dispatcher holds before a request walks on or is refused busy; it is read when an escrow's dispatcher starts. |

Four accessors carry rules of their own. `Server.AdminEnabled` is what callers must gate on — comparing a presented credential against `AdminAPIKey` directly would authenticate an empty one. `Limits.AccessFor` resolves any model `ModelAccess` does not name to admin-only, an empty map included, and `Limits.OpenModels` lists the open ones the gateway warns about. See [`docs/operations.md`](../docs/operations.md), "Who may call what". `Limits.ContextLength` takes the `--max-model-len` governance registers over the operator's `max_model_len` pin, reads a length above `MaxContextTokens` as none, and is `NoContextLength` when neither names one; the race judges a context refusal against it and the scheduler prices an escrow's retirement floor on it.

## The combinations `Validate` refuses

Most checks are ordinary range bounds. These are the ones that exist because a legal-looking pair is unsafe:

- `admin_api_key` must be at least 16 characters when set. A key short enough to guess is worse than none, because "none" disables admin entirely while a weak one authenticates.
- `api_keys` must be non-empty once any `model_access` entry is `api_key`, or that model is configured to be unreachable.
- `host_cutoff_max_ms` must not exceed `perf_ejection_max_seconds`, so [`perf`](../perf/) stays the dominant ejection authority rather than the per-host cutoff.
- `engine_loser_grace_ms` must be at least `engine_inter_chunk_stall_ms`. A loser is cancelled at the grace, so a grace under the stall window kills attempts that are merely between chunks — before the gateway would even call such a stream stalled.
- `scheduler_match_wait_ms` is capped at 5000. A long grace parks a committed-cost nonce on the chance of a co-arrival, so the ceiling is a budget guard, not a taste judgement.
- `scheduler_dispatch_queue_depth` is in [1, 65536]: each escrow's dispatcher allocates its queue up front, so the ceiling bounds that allocation; the waiters themselves are bounded by admission, since each holds its admission slot.
- `scheduler_max_consecutive_burns` is capped at 1000, which is past any real group size: a run longer than the group is a rung that never fires.
- `rotation_settle_margin_blocks` below 1.
- `chain_grpc` is checked as `host:port`, not as a URL: a gRPC target carries no scheme, so a URL check would pass anything.

Every problem is collected and reported together, and the field names in the messages use the snake_case admin-API spelling rather than the Go one.

## Using the `Holder`

Readers call `Load` on every use and get a shared, immutable pointer. Reconfiguration calls `Swap`, which publishes the whole replacement and then notifies subscribers **synchronously and in no particular order** — so a subscriber must be fast, must be order-independent, and must never call `Swap` itself.

## Overrides

`Overrides` is the admin-tunable subset that the store persists and `Build` merges over the environment layer; a nil field means "not overridden". `ParseOverrides` rejects unknown fields, because a typo in an admin `PUT` must be reported rather than silently ignored. `ParseStoredOverrides` is the read-back path and accepts them, because the file on disk may name a knob this build does not have: a knob it does not have costs the operator that knob, never the boot.
