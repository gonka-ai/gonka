# `config` — the immutable snapshot

Everything the gateway's behaviour depends on, in one value that is never mutated.

## What it owns

- **`Build`** — merges three layers in a fixed order: defaults, then environment, then the operator's stored overrides, and takes the active client keys as its last argument: they come from the store's `api_keys` table, never from the environment. It clones every map and slice it takes from its inputs, so nothing it returns shares memory with its sources.
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
| `Chain` | `GRPCEndpoint` is what the escrow bridge dials. `common/chain` derives the CometBFT RPC host from it at the standard port, and that derived endpoint is the fallback every chain call inherits, reads and the one-shot transaction broadcast alike — a deployment that moved the RPC port must set `RPCEndpoint` explicitly. `VersionsPollIntervalSeconds` (default 15, 1 to 86 400) is how often each candidate host's `/v1/versions` is polled ([`chain/README.md`](../chain/README.md)). |
| `Tx` | fee, gas and the poll loop that waits for a transaction. |
| `Limits` | admission tuning. A zero `MaxInputTokensInFlight` is unlimited. `MaxTokensCap` bounds what a client may *ask for* and does not clamp `DefaultMaxTokens`. `Access` is the tier a model with no tier of its own runs under (admin-only by default), and `MaxModelLen` is a model's own context-length pin. |
| `Limits.Concurrency` | a zero `MaxRequests` is no static cap at all, leaving admission to the capacity-scaled per-weight limit alone. |
| `Limits.HostWindows` | the per-host congestion windows, counted in requests of the model: a floor and a starting size for input and for output, and no ceiling — where a window stops is what the host's own congestion signals say. See [`docs/capacity.md`](../docs/capacity.md), "The participant limiter: IOCW". |
| `Limits.Congestion` | the factors a narrowing multiplies the blamed window by, the factor the other window takes with it, and how far above the best latency it has held a host may drift before a healthy answer counts as congestion. |
| `ModelSettings` | one model's overrides of the network-wide settings of the same name, held under `models.<id>`; a `nil` field inherits the network value rather than meaning zero. See "Per-model settings". |
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
| `Engine` | race-escalation tuning. A zero `MaxAttemptsPerRequest` is bounded only by the host group, and `MaxStartedAttemptsPerRequest` is how many attempts one request may start in all, hedges and retries included (0 turns that off), and the backstops this struct does not carry are engine constants — see [`docs/race.md`](../docs/race.md), "Tunables and backstops". |
| `Scheduler` | `MatchWaitMS` is how long a bound nonce waits for a co-arriving compatible request before it is burned; 0 burns immediately. `MaxConsecutiveBurns` is how many nonces one escrow may burn in a row before the next binding is sent over a full congestion window instead; 0 turns that off. `MaxBurnsPerRequest` is how many nonces one request may burn across all its attempts before the next binding is answered overloaded and burns nothing; 0 turns that off. `DispatchQueueDepth` is how many waiters one escrow's dispatcher holds before a request walks on or is refused busy; it is read when an escrow's dispatcher starts. |

Four accessors carry rules of their own. `Server.AdminEnabled` is what callers must gate on — comparing a presented credential against `AdminAPIKey` directly would authenticate an empty one. `Config.For(model).Limits.Access` is admin-only for any model with no tier of its own, an empty `models` included, and `Config.OpenModels` lists the open ones the gateway warns about. See [`docs/operations.md`](../docs/operations.md), "Who may call what". `Limits.ContextLength` takes the `--max-model-len` governance registers over the operator's `max_model_len` pin, reads a length above `MaxContextTokens` as none, and is `NoContextLength` when neither names one; the race judges a context refusal against it and the scheduler prices an escrow's retirement floor on it.

## Legacy inputs

- Legacy `model_limits` (deprecated, removal planned for v7 or v8): the settings key is still accepted and read as `models.<id>` with the same field names; set `models.<id>` instead, which wins field by field, and a boot or a write holding the key logs a warning.
- Legacy `model_access` (deprecated, removal planned for v7 or v8): the settings key is still read as `models.<id>.access`; set `models.<id>.access` instead, which wins when both name the model, and a boot or a write holding the key logs a warning.
- Legacy `DEVSHARD_API_KEYS` (deprecated, removal planned for v7 or v8): the variable is still read, comma-separated and each key at least 16 characters, and its keys are active beside the database keys; create them through `POST /v1/admin/api-keys` instead; `Server.EnvAPIKeys` holds them and `Server.APIKeys` is them followed by the database keys. The list and a read by id show them with `source` `env`, no value and no `created_at`, and they cannot be revoked or created again through the API (409): to remove one, move its clients to a new key, then unset the variable.

## The combinations `Validate` refuses

Most checks are ordinary range bounds. These are the ones that exist because a legal-looking pair is unsafe:

- `admin_api_key` must be at least 16 characters when set. A key short enough to guess is worse than none, because "none" disables admin entirely while a weak one authenticates. Every entry of `api_keys` is held to the same 16, since a short client key spends the operator's escrows for whoever guesses it. A client key must contain no whitespace, because no `Bearer` header can carry it, and must differ from the admin key, or a client request would be admitted with admin rights.
- `CheckClientKeys` refuses a network or per-model `access` of `api_key` while no client key is active, or that model is configured to be unreachable. `Validate` does not run it: a gateway with no keys must still boot to accept the first one, so boot logs a warning and only a key revoke or a settings write is refused.
- `participants.shadow` must be empty, for the network and for each model, while the effective `max_tokens_cap` is below `ProbeMinOutputTokens` (256): no probe can be built there, so a shadow nonce would hold the escrow's cursor until the waiters' deadlines.
- `host_cutoff_max_ms` must not exceed `perf_ejection_max_seconds`, so [`perf`](../perf/) stays the dominant ejection authority rather than the per-host cutoff.
- `engine_loser_grace_ms` must be at least `engine_inter_chunk_stall_ms`. A loser is cancelled at the grace, so a grace under the stall window kills attempts that are merely between chunks — before the gateway would even call such a stream stalled.
- `match_wait_ms` is capped at 5000. A long grace parks a committed-cost nonce on the chance of a co-arrival, so the ceiling is a budget guard, not a taste judgement.
- `scheduler_dispatch_queue_depth` is in [1, 65536]: each escrow's dispatcher allocates its queue up front, so the ceiling bounds that allocation; the waiters themselves are bounded by admission, since each holds its admission slot.
- `max_consecutive_burns` is capped at 1000, which is past any real group size: a run longer than the group is a rung that never fires.
- `max_burns_per_request` is capped at 1000 for the same reason: a cap above a request's lifetime of nonces never binds.
- `engine_max_started_attempts_per_request` is capped at 1000 for the same reason: a cap above the host group never binds.
- `rotation_settle_margin_blocks` below 1.
- `tx_call_timeout_ms` must be at most `tx_poll_timeout_ms`: a single chain call outlasting the whole commit wait it is part of would make the call, not the wait, the bound.
- `drain_timeout_seconds` must be at least `MinDrainTimeoutSeconds` (1200), the streaming hard timeout, so the drain never ends an attempt still within the deadlines the race armed for it ([`docs/race.md`](../docs/race.md), "Client departure and the drain").
- `chain_grpc` is checked as `host:port`, not as a URL: a gRPC target carries no scheme, so a URL check would pass anything.

Every problem is collected and reported together, and the field names in the messages use the snake_case admin-API spelling rather than the Go one.

## Per-model settings

`models.<id>` holds a `ModelSettings`: one optional field for each setting that is a property of a model, spelled with the network key (`max_burns_per_request`, `perf_ejection_max_seconds`, `host_cutoff_max_ms`, `access`, `max_tokens_cap`, ...), plus `max_model_len`, which only a model has: its pin is what the network's `fallback_max_model_len` prices a model with no length at. A field a model leaves out inherits the network value. `GATEWAY_MODELS_JSON` carries the same document for the environment layer, and the admin layer wins per model and per field.

`Build` resolves every named model into a full copy of the network `Config` with the model's fields laid over it, runs the whole of `Validate` on the copy, so every cross-field rule holds per model, and prefixes its problems with `models[<id>].`. `Config.For(model)` returns that copy, else the network config; every decision about a model reads it, and the participant limiter, the gateway limiter and the perf tracker keep one configuration per model.

The keys left out of `ModelSettings` belong to the process: listener, chain, wallet, storage, the shared buffers and caches, the funding loop, the vote budget, the staleness sweep and the dispatch queue depth. Posting one under `models.<id>` is a 400. `participants` is one such key: `models.<id>.participants.{trust,shadow,unthrottled}` sets that model's list, which the network list is never joined to, and `Config.ShadowFor(participant, model)` reads the effective one. A model id must be named by `rotation_models_json` when that list is set; with no list the build warns, because nothing visible can confirm the id. A stored block for a model the list no longer names does not stop the boot: the build warns and leaves it out of `For` and `Offers`, and `CheckStoredModels` refuses the next settings write with `ErrInvalid` until a `null` clears it, so an environment change never leaves the admin API unreachable.

## Using the `Holder`

Readers call `Load` on every use and get a shared, immutable pointer. Reconfiguration calls `Swap`, which publishes the whole replacement and then notifies subscribers **synchronously and in no particular order** — so a subscriber must be fast, must be order-independent, and must never call `Swap` itself.

## Overrides

`Overrides` is the admin-tunable subset that the store persists and `Build` merges over the environment layer; a nil field means "not overridden". A setting both layers can set lives once in `env.Tunables`, embedded in `env.Values` and `Overrides` and merged by `applyTunables` for each layer; its JSON keys stay at the top level. `ParseOverrides` rejects unknown fields, because a typo in an admin `PUT` must be reported rather than silently ignored. `ParseStoredOverrides` is the read-back path and accepts them, because the file on disk may name a knob this build does not have: a knob it does not have costs the operator that knob, never the boot. `Merged` merges inside `participants` per sub-key and inside `models` per model and per field, and a model set to `null` drops its admin block (the block `GATEWAY_MODELS_JSON` gives it stays), and `Participants.validate` refuses a participant in both `shadow` and a list that routes client traffic to it. See [`docs/operations.md`](../docs/operations.md), "The participants setting".
