# Configuration

Reference for everything that configures the gateway: the three layers a value comes from, the environment variables, the admin overrides and the per-model filter settings. Use it to find the key that controls a behaviour, its default and whether it can change without a restart. Procedures that use these keys are in the [runbook](./runbook.md).

## The three layers

Three layers, later wins:

1. **Defaults** — `config/defaults.go`, `Defaults()`. The only place a default lives.
2. **Environment** — read once at boot, in `env/` and nowhere else. `env.Load` returns *what is set* (a nil pointer is unset), so an unset variable can never overwrite a default with a zero.
3. **Admin overrides** — the fields of `config.Overrides`, written through `/v1/admin/settings`, persisted in the store and reloaded at boot. A `POST` is a patch over the stored overrides (`Overrides.Merged`): a field it names replaces the stored one, a field it sets to `null` is cleared back to the layers below, and every other field stays. A `PUT` is the whole document and replaces the stored one. Writes are applied one at a time, so two operators patching at once cannot lose each other's fields. They take effect without a restart: the config is an immutable snapshot swapped whole, and every reader loads it per request. The `settings written` audit line carries the method, the open models, the kill switch and the three `participants` lists as they stand after the call.

Parse failures are accumulated, so a boot reports **every** misconfigured variable at once rather than one per restart.

## Per-model settings

Every setting that applies to the whole network can also be set for one model, under `models.<id>` in `/v1/admin/settings`, with the same key as the network setting: `{"max_burns_per_request": 16, "models": {"MiniMaxAI/MiniMax-M2.7": {"max_burns_per_request": 8, "host_cutoff_max_ms": 60000}}}`. A field a model leaves out inherits the network value, and `max_model_len` is the one key only a model has. The same JSON in `GATEWAY_MODELS_JSON` sets them at boot; the admin value wins per model and per field. The whole configuration is validated again with each model's values laid over it, so a model that raises `host_cutoff_max_ms` past its own `perf_ejection_max_seconds` is refused as `models[<id>].host_cutoff_max_ms`. Process-wide keys (the kill switch, the buffers, the funding loop, `engine_max_concurrent_timeout_votes`, `perf_host_staleness_seconds`) are not accepted under `models`: a 400. A stored block for a model `rotation_models_json` no longer lists does not stop the gateway starting: the boot logs a warning and the block is ignored. Every settings write is then refused with a 400 naming the model until `{"models": {"<id>": null}}` clears it. [`config/README.md`](../config/README.md), "Per-model settings", has the key rules.

## Variable names

Each variable is read under one name only. `DEVSHARD_CHAIN_GRPC` and `DEVSHARD_CHAIN_RPC` fall back to `NODE_GRPC_URL` and `NODE_RPC_URL`.

Two grammars apply. The lenient one covers every `DEVSHARD_*` variable and six `GATEWAY_*` limits — `GATEWAY_DEFAULT_MAX_TOKENS`, `GATEWAY_MAX_TOKENS_CAP`, `GATEWAY_MAX_CONCURRENT_REQUESTS`, the two `_PER_10000_WEIGHT` limits and `GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT`. It reads booleans in devshard's shared grammar (`on`, `off`, `yes`, `no` as well as `true` and `false`), `DEVSHARD_STATS_TOKENIZER` among them, except the three height-sync switches, which keep their own spellings: `DEVSHARD_GATEWAY_CHAIN_ORACLE` and `DEVSHARD_HEIGHTSYNC_HEARTBEAT` are on only for `true`, `1` or `on`, and `DEVSHARD_REQUIRE_HEIGHT_SEED` is off only for `0`, `false`, `off` or `no`; `DEVSHARD_GATEWAY_HOST_PING_INTERVAL`, `_TIMEOUT`, `DEVSHARD_GATEWAY_RPC_STATS_INTERVAL`, `_TIMEOUT` and `DEVSHARD_QUIET_SETTLE_DELAY` as durations (`15s`); `DEVSHARD_POC_REQUEST_MODE` in any letter case. A lenient value that is unparsable, negative, or zero where zero means the default (`GATEWAY_DEFAULT_MAX_TOKENS`, `GATEWAY_MAX_TOKENS_CAP`, the two `_PER_10000_WEIGHT` limits, the transaction, cache, port and snapshot settings, `DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS`, `DEVSHARD_MAX_CONCURRENT_PROMPT_COUNTS`, `DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY` and `DEVSHARD_GATEWAY_RPC_STATS_CONCURRENCY`) is ignored with a warning naming the variable, so the default applies. A `GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT` set alone also sets the PoC limit unless it equals the default.

Every other `GATEWAY_*` variable is strict and refuses a bad value at boot. An **empty** value counts as unset.

Four defaults shape a first deployment: the nonce ledger (`DEVSHARD_STATS_ENABLED`) is off, `DEVSHARD_POC_REQUEST_MODE` is `relaxed`, `DEVSHARD_LOG_FORMAT` is `json`, and `GATEWAY_MAX_CONCURRENT_REQUESTS` is 2048.

`DEVSHARD_PUBLIC_API` must be an absolute URL (`config/validate.go`, `checkBaseURL`), so no value turns the public API off: the phase observer needs it.

Signing keys are addressed **by the name of the variable that holds them**, never by value: `escrows_json` and `rotation.models_json` carry `private_key_env`. Log lines and errors name the variable, never the key.

## The knobs that decide behaviour

| Variable | Default | What it decides |
| --- | --- | --- |
| `DEVSHARD_PORT` | 8080 | the listening port |
| `DEVSHARD_CHAIN_ID` | empty | the chain id the gateway signs its transactions for; empty asks the node over `DEVSHARD_CHAIN_GRPC` or its RPC fallback, and a configured value wins over the node; a node whose `app.toml` has `grpc.enable` and `api.enable` both false cannot answer it, so set it there ([`chain/README.md`](../chain/README.md), "The gRPC transport") |
| `DEVSHARD_STORAGE_DIR` | `$HOME/.cache/gonka-gateway` | where `gateway.db` and the escrow storage live |
| `DEVSHARD_SESSION_RETENTION_EPOCHS` | 2 | how many epochs before the effective one an escrow's session directory stays on disk; `0` keeps every one, and `1` is raised to 2, the hosts' own horizon ([operations.md](./operations.md), "Session storage") |
| `GATEWAY_MAX_CONCURRENT_REQUESTS` | 2048 | the hard admission ceiling; `0` sets no static cap and lets the weight model decide alone |
| `GATEWAY_ADMISSION_QUEUE_WAIT_MS` | 300000 | how long a request waits for a slot before 429 |
| `GATEWAY_ADMISSION_QUEUE_PER_SLOT` | 4 | how deep the queue is allowed to grow per slot |
| `GATEWAY_MAX_BUFFERED_RESPONSE_BYTES` | 512 MiB | **every** non-streaming reply being assembled, at once |
| `GATEWAY_MAX_BUFFERED_REQUEST_BYTES` | 128 MiB | **every** chat body being read and normalised, at once; a body past it is answered 503; at least 10 MiB ([`request.md`](request.md), "Order of operations") |
| `GATEWAY_MIN_CLIENT_READ_BYTES_PER_SECOND` | 64 KiB/s | the slowest average a client may read one reply at, after a 30-second allowance, before its write fails; between 1 B/s and 1 GiB/s ([`api/README.md`](../api/README.md), "Streaming the reply") |
| `DEVSHARD_CHAT_CACHE_MAX_BYTES` | 256 MiB | the response cache |
| `GATEWAY_DEFAULT_MAX_TOKENS` / `GATEWAY_MAX_TOKENS_CAP` | from `filters` | the output budget a request gets and may ask for |
| `GATEWAY_MODELS_JSON` | empty | the per-model settings the environment layer sets, a JSON object keyed by model id ("Per-model settings" above) |
| `DEVSHARD_ESCROW_ROTATION_ENABLED` | false | whether the epoch bridge creates and retires escrows |
| `DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED` | false | whether retirement settles or only parks; a model's own `settlement_enabled` in the models JSON overrides it |
| `DEVSHARD_ESCROW_ROTATION_PREVIOUS_EPOCH_SETTLEMENT_ENABLED` | false | settles parked and deadline escrows once their chain epoch is over, never inside it; a model's own `previous_epoch_settlement_enabled` in the models JSON overrides it |
| `GATEWAY_ROTATION_SETTLE_MARGIN_BLOCKS` | 600 | blocks before an escrow's settlement deadline at which it is parked for settling; at least 1; a margin the measured block time (over at least 100 blocks) makes shorter than two settle windows is journalled as `settle margin shorter than two settle windows`, once per episode; the margin is never refused |
| `DEVSHARD_ESCROW_ROTATION_PRE_POC_BLOCKS` | 300 | how early the bridge starts |
| `DEVSHARD_ESCROW_ROTATION_MODELS_JSON` | empty | the models rotation keeps escrows for, each with `model_id`, `temp_count`, `amount` and `private_key_env`; optional are `target_count` (1), `reserve_count` (1, `0` turns reserves off), `max_unsettled` (the target, temp and reserve counts plus 4), `full_context_slots` (2) and the model's own `settlement_enabled` and `previous_epoch_settlement_enabled`; empty rotates no model |
| `GATEWAY_WARM_NEW_ESCROWS` | true | whether a new escrow is taught to its group before serving |
| `GATEWAY_CHAIN_SNAPSHOT_MAX_AGE_SECONDS` | 60 | how stale the chain snapshot may be before requests are refused 503; `0` disables the gate |
| `GATEWAY_CHAIN_VERSIONS_POLL_INTERVAL_SECONDS` | 15 | how often each candidate host's `/v1/versions` is polled for PoC-validation capability; 1 to 86 400; a host's last good answer lives three intervals |
| `GATEWAY_ENGINE_RECEIPT_TIMEOUT_MS` | 5 000 | receipt deadline; doubled above 100 000 input tokens |
| `GATEWAY_ENGINE_FIRST_TOKEN_FLOOR_MS` | 6 000 | lower bound on the first-token curve |
| `GATEWAY_ENGINE_FIRST_TOKEN_CEILING_MS` | 30 000 | upper bound, whatever the host's own p75 asks for |
| `GATEWAY_ENGINE_INTER_CHUNK_STALL_MS` | 30 000 | silence after first content before an attempt is stalled |
| `GATEWAY_ENGINE_LOSER_GRACE_MS` | 600 000 | how long a loser may keep running after the crown |
| `GATEWAY_ENGINE_HEDGE_FIRST_TOKEN_FLOOR_MS` | 1 500 | lower bound on the slow-start hedge that starts a second attempt before the judged deadline; `0` turns it off |
| `GATEWAY_ENGINE_MAX_ATTEMPTS_PER_REQUEST` | 2 | unfinished attempts one request may hold at once; `0` bounds it only by the host group ([race.md](./race.md), "Tunables and backstops") |
| `GATEWAY_DRAIN_TIMEOUT_SECONDS` | 2 400 | how long a race may run after its client leaves; at least 1 200 ([race.md](./race.md), "Tunables and backstops") |
| `GATEWAY_ENGINE_MAX_STARTED_ATTEMPTS_PER_REQUEST` | 3 | attempts one request may start in all, hedges and retries included; past it no hedge or retry starts, and the request is answered overloaded when no attempt of it is alive; `0` never caps ([`routing.md`](routing.md), "The burn cap of a request") |
| `GATEWAY_ENGINE_MAX_CONCURRENT_TIMEOUT_VOTES` | 64 | chain votes for unfinished nonces being posted at once, not a bound on settling escrows; one past it waits for a poster rather than being dropped |
| `DEVSHARD_STATS_ENABLED` | false | the per-nonce ledger and its JSON API on `DEVSHARD_STATS_PORT` (9091), on every interface, with 10 s read, 60 s write and 120 s idle timeouts; the ledger's diff observer is also what removes a cached reply on a negative validation vote |
| `DEVSHARD_STATS_SNAPSHOT_SECONDS` | 60 | how often the nonce ledger writes its snapshot to disk, and so the most a crash can roll it back; a value below 1 is ignored with a warning |
| `DEVSHARD_STATS_RETENTION_EPOCHS` | 2 | how many epochs before the current one the nonce ledger keeps retired escrows; `0` keeps every epoch |
| `GATEWAY_PERF_EWMA_HALFLIFE_SECONDS` | 600 | how fast a host's history forgets |
| `GATEWAY_TIMEOUT_SWEEP_BUDGET_PER_TICK` | 8 | execution-timeout votes one tick may retry across every escrow, the most overdue first; `0` turns the sweep off |
| `GATEWAY_TIMEOUT_SWEEP_GRACE_SECONDS` | 120 | how far past its deadline a nonce must be before the sweep claims it from its own race |
| `DEVSHARD_POC_REQUEST_MODE` | relaxed | `relaxed` keeps serving through proof-of-compute; `off` refuses new requests while the chain blocks them; in either mode the funding planner funds through a confirmation PoC and pauses only for an epoch PoC |
| `GATEWAY_HEIGHT_SYNC_ENABLED` | true | whether the escrow's heartbeat cadence runs. Turn it off for a fleet whose hosts do not carry height sync, or every interval logs a heartbeat it could not stamp ([`docs/escrows.md`](escrows.md), "Height sync") |
| `DEVSHARD_HEIGHTSYNC_HEARTBEAT` | false | whether a quiet escrow composes heartbeat turns; on only for true/1/on. A finished request's leftovers settle either way ([`docs/escrows.md`](escrows.md), "Height sync") |
| `DEVSHARD_REQUIRE_HEIGHT_SEED` | true | whether a session chases a host-signed tip from the moment it opens and reports `seed_incomplete` when it cannot. It does not refuse requests — see [`docs/escrows.md`](escrows.md), "Height sync". Off in the e2e stand |
| `DEVSHARD_HEIGHTSYNC_K` / `DEVSHARD_HEIGHTSYNC_SLOTS` | 10 / 1 | the anchor cadence carried on the height-sync envelope |
| `DEVSHARD_QUIET_SETTLE_DELAY` | 5m | how long a session keeps a finished request's leftovers for the next request before composing them on its own nonce; the test stand sets `5s` |
| `DEVSHARD_GATEWAY_CHAIN_ORACLE` | false | whether the gateway follows mainnet itself, from `NODE_MANAGER_ADDR`, the chain RPC and gRPC. It is not what the gateway stamps from — it labels how much a carried tip is trusted, and it serves the block-time lookups when no commit-verified source is configured ([`heights/README.md`](../heights/README.md)) |
| `DEVSHARD_NODE_MANAGER_ADDR` / `NODE_MANAGER_ADDR` | localhost:9400 | node-manager's address, resolved once (`runtimeparams.SettingsFromEnv`) for both its readers: the runtime-params feed and, when it is on, the height follower. Unreachable is not fatal to either — the feed polls the chain instead, less promptly, and the follower fails over to the chain RPC and gRPC |
| `DEVSHARD_PARAMS_SOURCE` | auto | which feed answers for governance: `auto` prefers the long poll with a chain fallback, `chain` polls the chain alone and dials no node manager |
| `DEVSHARD_GATEWAY_HOST_PING_DISABLED` | false | whether the hosts the live escrows use are pinged for reachability and clock drift. Observability only — a host that stops answering a ping keeps its routing weight ([`hostping/README.md`](../hostping/README.md)) |
| `DEVSHARD_GATEWAY_HOST_PING_INTERVAL` / `DEVSHARD_GATEWAY_HOST_PING_TIMEOUT` | 15s / 2s | the probe cadence and its patience; the timeout must be at most half the interval or the ping is refused and switched off |
| `DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY` | 8 | hosts probed at once within one wave |
| `DEVSHARD_GATEWAY_RPC_STATS_DISABLED` | false | whether the hosts the live escrows use are polled for their rate limiters' counters of the last closed minute. Observability only — a host whose limiter refuses this gateway keeps its routing weight ([`hostrpc/README.md`](../hostrpc/README.md)) |
| `DEVSHARD_GATEWAY_RPC_STATS_INTERVAL` / `DEVSHARD_GATEWAY_RPC_STATS_TIMEOUT` | 15s / 4s | the poll cadence and its patience per host; the timeout must be at most the interval or the poll is refused and switched off |
| `DEVSHARD_GATEWAY_RPC_STATS_CONCURRENCY` | 8 | hosts polled at once within one wave |
| `DEVSHARD_ALLOW_PRIVATE_ADDRESSES` | false | whether dials to private addresses are allowed. A host URL comes from chain state, so the guard is on in production and only a stand whose hosts are Docker names turns it off; turning it off is logged |
| `DEVSHARD_OTEL_ENABLED` | false | whether OpenTelemetry traces are exported; off is a no-op tracer ([observability.md](./observability.md), "Tracing") |
| `OTEL_ENDPOINT` / `OTEL_HEADERS` | unset | the OTLP gRPC collector and its headers as `key=value,key=value`; a malformed header is skipped with a warning |
| `DEVSHARD_LOG_FORMAT` | json | one JSON object per line, which promtail and the Loki panels read; any other value is the text form |

## The rest of the variables

These are read the same way and rarely need changing. Where a knob has a longer explanation, the last column points at it.

| Variable | Default | What it decides |
| --- | --- | --- |
| `DEVSHARD_GATEWAY_DISABLED` | false | the kill switch ([operations.md](./operations.md), "The kill switch") |
| `DEVSHARD_GATEWAY_DISABLED_MESSAGE` | empty | the text of the 503 a disabled gateway answers ([operations.md](./operations.md), "The kill switch") |
| `DEVSHARD_ENABLE_CONTENTION_PROFILES` | false | whether lock contention and blocking are sampled, so `/debug/pprof/mutex` and `/debug/pprof/block` hold data; it costs a little on every contended lock, so it stays off outside a diagnosis, and turning it on is logged |
| `DEVSHARD_GATEWAY_DISABLED_NEW_URL` | empty | the URL a disabled gateway redirects to with 308; empty means 503 |
| `DEVSHARD_ADMIN_API_KEY` | empty | the admin key; a key shorter than 16 characters is refused, and an empty one leaves the admin surface unrouted ([operations.md](./operations.md), "Who may call what"); client keys are not set here, bar the legacy variable ([operations.md](./operations.md), "Legacy inputs") |
| `DEVSHARDS_JSON` | empty | the escrows seeded into the store at boot, each naming its `id` (the chain escrow id in canonical decimal; any other fails the boot), `model` and `private_key_env` ([operations.md](./operations.md), "Boot") |
| `DEVSHARD_CHAIN_GRPC` / `DEVSHARD_CHAIN_RPC` | `localhost:9090` / empty | the chain's gRPC endpoint, and the CometBFT RPC every chain call falls back to when gRPC is unreachable, queries and transactions alike; the gateway runs with gRPC unreachable as long as `DEVSHARD_CHAIN_RPC` points at a live CometBFT RPC, and a transaction broadcast runs once on the transport the reads selected; an empty RPC endpoint is derived from the gRPC host at the standard port ([`app/README.md`](../app/README.md), "Escrow sessions and the chain connection"), and that resolved endpoint is also where block header times are read and commit-checked first (a derived endpoint that does not answer at startup is dropped for the unverified read, an explicit one is kept) ([`heights/README.md`](../heights/README.md)) |
| `DEVSHARD_PUBLIC_API` | `http://localhost:9000` | the public API the phase observer polls; it must be an absolute URL |
| `DEVSHARD_RPC_H2_UPGRADE` | on | whether a session's connection to a host first tries HTTP/2 on the origin of the host's inference URL (h2c on `http`, TLS offering ALPN `gonka-rpc/1` on `https`) and drops to Connect over HTTP/1.1 on the same origin when the host does not speak it; `0`, `false`, `no` or `off` stays on HTTP/1.1 |
| `DEVSHARD_RPC_GRPC` | off | whether that HTTP/2 connection speaks native gRPC instead of Connect; on only for `1`, `true`, `yes` or `on`, and never used over HTTP/1.1 |
| `DEVSHARD_RPC_MAX_CONNS_PER_PEER` | 2 561 | the most HTTP connections the gateway keeps open to one host, of which at most 128 stay idle and the rest close after use; a value that is not a positive integer falls back to the default without a warning |
| `DEVSHARD_RPC_ENDPOINTS` | unset | not honoured: every peer method uses Connect, and a set value is logged once as ignored |
| `DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS` | 16 | escrow sessions opened at once while the boot publishes the active escrows ([operations.md](./operations.md), "Boot") |
| `DEVSHARD_MAX_CONCURRENT_PROMPT_COUNTS` | half of `GOMAXPROCS`, at least 1 | prompts tokenized at once, after the cache lookup and at the context check; a request that finds every slot taken after the cache lookup is not held but carries the byte estimate instead, while the context check waits for a slot ([`request.md`](request.md), "The prompt token count") |
| `DEVSHARD_STATS_TOKENIZER` | off | whether every request that admission lets through and the cache does not answer is counted with the model's tokenizer after the cache lookup, through the gate above, with each pre-token's merge capped at 512 bytes; off, only a prompt the context check tokenizes carries a tokenizer count and every other request the byte estimate ([`request.md`](request.md), "The prompt token count") |
| `DEVSHARD_TX_FEE_DENOM` / `DEVSHARD_TX_FEE_AMOUNT` | `ngonka` / 1 000 000 | the fee every chain transaction of the gateway carries; a fee in `ngonka` is added to what an escrow create must find in the wallet |
| `DEVSHARD_TX_GAS_LIMIT` | 700 000 | the gas limit of those transactions |
| `DEVSHARD_TX_POLL_INTERVAL_MS` / `DEVSHARD_TX_POLL_TIMEOUT_MS` | 2 000 / 45 000 | how often a broadcast transaction is looked up, and how long before the gateway stops waiting for it |
| `DEVSHARD_TX_CALL_TIMEOUT_MS` | 10 000 | the deadline of each chain call behind escrow creates, settlements and the escrow tick's lookups, so a node that never answers fails the call instead of holding the tick; at most `DEVSHARD_TX_POLL_TIMEOUT_MS` ([`chain/README.md`](../chain/README.md), "Transaction encoding") |
| `GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT` | 5 | the requests a host's weight buys it, and with them the fleet's own ceiling ([`capacity.md`](capacity.md)) |
| `GATEWAY_POC_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT` | 10 | the same figure while the chain reports requests blocked for proof-of-compute |
| `GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT` | 0 | the per-model budget of input tokens in flight, scaled by capacity, in tokenizer tokens for a request counted with the model's tokenizer and in byte-estimate tokens otherwise ([`request.md`](request.md), "The prompt token count"); `0` is unlimited |
| `GATEWAY_MATCH_WAIT_MS` | 2 000 | how long a bound nonce waits for a compatible request before it is burned; `0` burns at once, and 5 000 is the most it accepts |
| `GATEWAY_MAX_CONSECUTIVE_BURNS` | 2 | nonces one escrow may burn in a row before the next one is sent over a full congestion window; `0` never forces a send ([`routing.md`](routing.md), "The forced send") |
| `GATEWAY_MAX_BURNS_PER_REQUEST` | 16 | nonces one request may burn across all its attempts; an escrow whose walk to a usable host costs more burns than the request has left is not picked, and past it the next binding burns nothing and sends nothing; the request is answered overloaded when no attempt of it is alive; `0` never caps ([`routing.md`](routing.md), "The burn cap of a request") |
| `GATEWAY_DISPATCH_QUEUE_DEPTH` | 1 024 | waiters one escrow's dispatcher holds; past it an unpinned request walks to the next escrow, and when every escrow of the model is full it is refused busy (503); 1 to 65 536, read when an escrow's dispatcher starts |
| `GATEWAY_FORCE_UPSTREAM_STREAMING` | true | whether a non-streaming request is still streamed from the host and assembled by the gateway ([`request.md`](request.md)) |
| `GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE` | unset | forces the hosts' logprobs optimization on or off for every inference; unset leaves each host on its own default (`DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED`, on). The admin override `logprobs_optimization` takes precedence, and removing it falls back to this value |
| `GATEWAY_REQUESTS_RETENTION_HOURS` / `GATEWAY_REQUESTS_RETENTION_MAX_ROWS` | 168 / 1 000 000 | how long and how many request records are kept; neither may be zero |
| `DEVSHARD_REQUEST_CAPTURE_ENABLED` | false | whether sampled requests are written to capture files ([`api/README.md`](../api/README.md), "Request capture") |
| `DEVSHARD_REQUEST_CAPTURE_DIR` | `<storage dir>/captured-requests` | where the capture files go |
| `GATEWAY_CAPTURE_SAMPLE_RATE` | 0.01 | the share of requests captured, from `0` (none) to `1` (every one), taken as a fixed stride rather than at random |
| `GATEWAY_CAPTURE_MAX_BYTES` | 1 GiB | what the capture directory may hold; nothing deletes capture files, so at the cap capture stops until the directory is emptied |
| `GATEWAY_PERF_CONSECUTIVE_FAIL_THRESHOLD` | 5 | failures in a row that eject a host from routing ([`capacity.md`](capacity.md)) |
| `GATEWAY_PERF_FAILURE_RATE_THRESHOLD` / `GATEWAY_PERF_FAILURE_RATE_MIN_VOLUME` | 0.15 / 20 | the failure rate that ejects a host, and the volume it must have seen before the rate counts |
| `GATEWAY_PERF_EJECTION_BASE_SECONDS` / `GATEWAY_PERF_EJECTION_MAX_SECONDS` | 30 / 60 | how long an ejection lasts, the first time and at most; the maximum is at least the base and at least `host_cutoff_max_ms` |
| `GATEWAY_PERF_MAX_EJECTION_FRACTION` / `GATEWAY_PERF_MIN_AVAILABLE_HOSTS` | 0.5 / 4 | the share of a model's hosts that may be ejected at once, and the number that always stays routable |
| `GATEWAY_PERF_HOST_STALENESS_SECONDS` | 3 600 | how long an unseen host is remembered before its history and its participant-labelled metric series are dropped |

`env/env.go` remains the authoritative list and `config.Defaults()` the authoritative set of defaults; a variable added there belongs in one of the two tables above. Three sets sit outside `env/env.go`, because they are the same knobs every devshard binary honours: the runtime-params feed reads the fleet-shared `DEVSHARD_*` / `DEVSHARDD_*` knobs declared in `devshard/runtimeparams/env.go`, a session's connection to its hosts reads the four `DEVSHARD_RPC_*` knobs above, declared in `devshard/transport`, and tracing reads `DEVSHARD_OTEL_ENABLED` in `devshard/observability` and `OTEL_ENDPOINT` / `OTEL_HEADERS` in `common/observability`.

## Model filters

### Configuring a model's filters

Each model's request filters are a preset, filters set over it, or both (`api/admin_filters.go`). A model nobody configured runs its built-in binding — Kimi-K2.6 `kimi`, MiniMax-M2.7 `minimax`, GLM-5.3-Flash `glm_flash` — or `default`. A setting applies to the next request; no restart.

```sh
curl -X PUT -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  "$GATEWAY/v1/admin/filters/Qwen/Qwen3-235B-A22B-Instruct-2507-FP8" \
  -d '{"preset":"glm_flash","filters":{"output_tokens_floor":32}}'
```

The answer is the model's entry: `source` (`stored`, `built_in`, `default`, or `unreadable` for a stored setting the gateway could not read at boot, with its `problem`), `preset`, the `filters` as stored, the `effective` filters, `on_chain`, `updated_at`, the read-only `limits` and any `warnings`. `GET /v1/admin/filters` lists every model the gateway knows of — stored, built-in and every model the chain registers — and `GET /v1/admin/filters/presets` the presets. `DELETE /v1/admin/filters/{model}` returns the model to its built-in binding.

Every filter a built-in preset uses is a knob; [`filters/README.md`](../filters/README.md), "Knobs", lists all of them with their bounds. The ones a new model most often needs:

| Filter | Values | What it does |
| --- | --- | --- |
| `thinking` | `normalize`, `mirror_to_kwargs`, `strip`, `force_on` | where `thinking` / `enable_thinking` go: normalised in place, mirrored into `chat_template_kwargs`, removed, or forced on |
| `thinking_template_key` | a kwargs key | which `chat_template_kwargs` key the template reads thinking from (`thinking` for Kimi, `enable_thinking` otherwise) |
| `thinking_token_budget` | `{force_zero_below, default_divisor, absolute_max, content_headroom}` | lets `thinking_token_budget` through and resolves it; registered models only |
| `empty_answer_counts_tokens` | bool | an empty answer that reports completion tokens counts as the model's, not the host's; registered models only |
| `reasoning_effort` | `{mode: forward or strip, allowed: [...]}` | forwards, strips or narrows `reasoning_effort` |
| `sampling` | `temperature_max`, `top_p_max`, `top_k_max`, `min_p_max`, `repetition_penalty_max` | lowers the global ranges for this model |
| `template_kwargs` | `{forced: {...}, denied: [...]}` | kwargs the template always gets, or never sees |
| `tools`, `parallel_tool_calls`, `reject_json_schema` | `allow`/`reject`, `keep`/`strip`, bool | for a model served without tool calling or schema-guided decoding |
| `force_zero_penalties`, `reject_structured_outputs`, `allow_safety_identifier`, `keep_reasoning_split`, `default_reasoning_split`, `output_tokens_floor` | as named | Kimi's and MiniMax's deltas, open to any model |

A new model with a Kimi-like template but its own key and a cooler temperature is one call:

```sh
curl -X PUT -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  "$GATEWAY/v1/admin/filters/acme/Reasoner-2" \
  -d '{"preset":"kimi","filters":{"thinking_template_key":"enable_thinking","sampling":{"temperature_max":1.2}}}'
```

A PUT that names no preset changes the model's own preset: `{"filters":{"output_tokens_floor":32}}` on Kimi keeps everything else Kimi's preset does. Name `"preset":"default"` to start from the default instead.

The entry also shows the model's token limits under `limits`, with `"source": "models"` when the model's default or cap differs from the gateway-wide one (set under `models` in `/v1/admin/settings` or `GATEWAY_MODELS_JSON`) and `"global"` when it runs the gateway-wide default and cap (`GATEWAY_DEFAULT_MAX_TOKENS`, `GATEWAY_MAX_TOKENS_CAP` or their settings keys). A filter PUT cannot change them.

A PUT checks that the chain registers the model. An unknown model answers 404 `model_not_on_chain`, and a gateway that has not read the chain's model list yet answers 503 `chain_models_unavailable`. `?allow_off_chain=true` skips the check, for a model that is not registered yet; the answer then carries `on_chain: false`. A setting with `thinking_token_budget` or `empty_answer_counts_tokens` is refused off chain even with the flag, and its audit line carries `engine_crash_risk=true` when the model has no built-in budget. A model that leaves the chain keeps its setting.

### Which setting wins

A model's request is shaped by several sources. Each owns its own part, and where two touch the same value, the order below decides. Nothing here is a tie: the first source that applies wins.

**Which filters a model runs** (`filters.ProfileSet.For`):

| Order | Source | Applies when |
| --- | --- | --- |
| 1 | the operator's stored setting (`PUT /v1/admin/filters/{model}`) | a row exists and the gateway could read it at boot |
| 2 | the model's built-in preset (`models` in `filters/presets/<name>.json`) | no readable row |
| 3 | `filters/presets/default.json` | neither |

Within one setting: a knob the setting sets beats the preset it names; a setting that names no preset is composed over the model's built-in preset (order 2), else `default`. A setting the gateway could not read at boot is not applied at all: the model runs order 2 or 3, and the entry shows `source: unreadable` with the `problem`, until the operator replaces or deletes it.

**What a request finally carries:**

| Value | Who decides, highest first | Why |
| --- | --- | --- |
| request invariants: `n`, `top_logprobs` ≤ 5, `return_token_ids`, `stop_token_ids` vocabulary, schema and `chat_template_kwargs` bounds, unknown-parameter refusal | the gateway's own rules, then the host, which re-pins the same values | they protect reservation accounting, validation and vLLM; no setting can loosen them |
| `max_tokens` the client named | `max_tokens_cap` (the model's own under `models`, else `GATEWAY_MAX_TOKENS_CAP`), then `output_tokens_floor` | the floor raises first and the cap clamps after, so **the cap wins**: it prices the reservation. An admin key skips the cap but not the floor |
| `max_tokens` the client left out | `default_max_tokens` (the model's own under `models`, else `GATEWAY_DEFAULT_MAX_TOKENS`), raised to `output_tokens_floor` but never by the floor above the cap | a default below the floor would hand the model the budget the floor exists to prevent; the floor still cannot reserve more than the cap prices. A default set above the cap is the operator's own choice and is kept. An admin key skips the cap here too |
| `thinking_token_budget` | the model's budget, resolved against the final `max_tokens` | it reads the resolved value: a cap below `force_zero_below` silences thinking on every request that names a budget, and a default below it on every request that names none |
| sampling ranges | the global bound, then the model's `sampling` limit | a model limit only narrows |
| who may call the model, whether it is routed | `access` (the model's own under `models`, else the network one), `rotation_models_json`, escrows | filters never open or route a model; a filter refusal (for example `tools: reject`) is answered before the access check, as every filter refusal is |
| context length | the chain's `--max-model-len`, else `models.<id>.max_model_len` | filters do not read it; a host refuses an over-long request and the engine retries elsewhere |
| streaming upstream, logprobs handling | `GATEWAY_FORCE_UPSTREAM_STREAMING`, `GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE` and their settings keys | independent of filters |

A filter entry lists `warnings` when the settings' limits override its filters — a cap below the floor, a cap below the budget's `force_zero_below`, or a default budget below it — so the conflict is visible where the filters are edited. Neither PUT refuses it: the two are edited separately and either order must be possible.

A cached reply is keyed on the normalized body, the model and the empty-answer exemption, so a filter change never serves a reply produced under the old filters; entries also expire within the cache TTL. A cached reply is also removed when its nonce is voted invalid (seen only while the nonce ledger, `DEVSHARD_STATS_ENABLED`, is on), when the chain's effective epoch rises, and when its participant is marked suspicious; a reply a suspicious host wins is not stored; a client can skip it per request with `Cache-Control: no-cache` or `no-store` ([`api/README.md`](../api/README.md), "What the client controls").

### When a release changes the presets

Presets are code: a release can change them, and the gateway applies the change at boot.

- **A model on its built-in preset** runs the new values at once.
- **A stored setting** keeps only its preset name and the knobs it sets. At boot it is recomposed over the release's preset, so it follows every value it does not set itself and keeps every value it does. A setting that named no preset follows the model's current built-in preset; if a release rebinds the model, the setting's base moves with it.
- **A stored setting that no longer reads** — its preset was renamed or removed, a knob it sets was renamed, or a new rule refuses its combination — is not applied. The gateway logs a warning at boot, the model runs its built-in preset, and the entry shows `source: unreadable` with the reason. Re-PUT the setting in the new form or DELETE it.
- **A preset file the release cannot load** stops the release at its own tests; it never reaches a gateway.

To pin a model to exact values across releases, PUT every knob that matters to you; a preset you name only sets the values you leave out.

### A new version of a model

The chain names a new version with a new model id. Every setting is keyed by the exact id — filter settings, built-in bindings, `models` — so nothing carries over: the new id runs the `default` preset and is admin-only until it is configured. Configure it with one PUT that names the preset of the version it replaces, and give it its own block under `models` in `/v1/admin/settings`.

## Where to change what

| To change | Go to |
| --- | --- |
| a default | `config/defaults.go`, `Defaults()` |
| which variables are read | `env/env.go` — and nowhere else |
| what an admin may override at runtime | `config.Overrides` |
| a route or its auth tier | `api/routes.go`, `routes()` |
| what shuts down when | `app/lifecycle.go`, `shutdownOrder` |
| a metric or its labels | `metrics/` |
