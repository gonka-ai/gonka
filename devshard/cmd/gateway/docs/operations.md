# Operating the gateway

One binary, one SQLite file, one HTTP port. Everything else — escrows, hosts, epochs — is read from the chain and rebuilt at boot. This document is what an operator needs: what is exposed, what can be changed at runtime, what the process does on the way up and down, and what to look at when it misbehaves.

## What is exposed

Three tiers, and the tier decides both who may call it and whether the kill switch applies:

| Route | Auth | Survives the kill switch |
| --- | --- | --- |
| `/v1/chat/completions` | per-model (below) | no |
| `/v1/models` | none — it lists what is routable, nothing caller-specific | no |
| `/v1/status` | none, as above | yes — the deployed compose healthcheck reads it, and a gateway disabled on purpose is not unhealthy |
| `/devshard/{id}/v1/chat/completions`, `.../models`, `.../status` | as above, pinned to one escrow | no |
| `/metrics`, `/healthz` | none | yes |
| `/v1/requests/{id}` | admin | yes |
| `/devshard/{id}/v1/finalize`, `.../state`, `.../debug/*` | admin | yes |
| `/v1/admin/*`, `/v1/debug/rotation`, `/v1/debug/memstats`, `/v1/debug/heightsync` | admin | yes |
| `/debug/pprof/*` | admin | yes |

The `/devshard/{id}/…` prefix pins a request to one escrow instead of letting the scheduler choose — the recovery surface for an escrow that needs attention on its own. A pinned chat to an escrow the gateway no longer routes answers 404, as an unknown devshard.

`GET /devshard/{id}/v1/debug/signatures` lists the slots that signed each nonce the session still holds signatures for — the retained window plus each validator's highest — so the view stays bounded on a long-lived escrow (`user/session.go`, `dropSignaturesThroughLocked`).

`POST /devshard/{id}/v1/debug/signatures/collect?nonce=N` asks the escrow's group again for its signatures at nonce `N`, which must not be ahead of the session's own, and answers `sig_weight`, `quorum_threshold`, `total_slots` and `has_quorum`. It reaches a live or draining escrow only: the signatures land in that session, which is the one a settlement builds its payload from; a settled or non-resident escrow answers 404. The call is written to the admin audit log.

`/devshard/{id}/v1/debug/inferences` lists every record of the escrow, the sealed ones included: an escrow that settled has sealed all of them, and a live-only view reads as one that served nothing. Sealed records are read back from storage under the state's read lock, so on a large escrow it is a slow call; `/devshard/{id}/v1/debug/pending` reads the live records alone, since a sealed record is never unresolved.

`/debug/pprof/` is the Go runtime profiler (`net/http/pprof`) behind the admin key: the index and every named profile (`goroutine`, `heap`, `allocs`, `mutex`, `block`, `threadcreate`), plus `cmdline`, `profile`, `symbol` and `trace`. It carries no route label, so profiling never shows in the request metrics. A binary profile is fetched with the admin header and read with `go tool pprof`, since the tool cannot send the header itself; `?debug=1` or `?debug=2` on a named profile answers plain text. `profile` and `trace` hold the request for their `seconds` and load the process while they run. `mutex` and `block` stay empty unless `DEVSHARD_ENABLE_CONTENTION_PROFILES` is on.

`/v1/debug/memstats` reads the heap, stack and GC figures through `runtime/metrics` (`api/memory.go`, `readMemory`), which never stops the world, so polling it under load costs no pause; a runtime sample of an unexpected kind answers 500 rather than a zero.

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

The entry also shows the model's token limits under `limits`, with `"source": "model_limits"` when the model has its own entry in `/v1/admin/settings` and `"global"` when it runs the gateway-wide default and cap (`GATEWAY_DEFAULT_MAX_TOKENS`, `GATEWAY_MAX_TOKENS_CAP` or their settings keys). A filter PUT cannot change them.

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
| `max_tokens` the client named | `max_tokens_cap` (`model_limits` in `/v1/admin/settings`, else `GATEWAY_MAX_TOKENS_CAP`), then `output_tokens_floor` | the floor raises first and the cap clamps after, so **the cap wins**: it prices the reservation. An admin key skips the cap but not the floor |
| `max_tokens` the client left out | `default_max_tokens` (`model_limits`, else `GATEWAY_DEFAULT_MAX_TOKENS`), raised to `output_tokens_floor` but never by the floor above the cap | a default below the floor would hand the model the budget the floor exists to prevent; the floor still cannot reserve more than the cap prices. A default set above the cap is the operator's own choice and is kept. An admin key skips the cap here too |
| `thinking_token_budget` | the model's budget, resolved against the final `max_tokens` | it reads the resolved value: a cap below `force_zero_below` silences thinking on every request that names a budget, and a default below it on every request that names none |
| sampling ranges | the global bound, then the model's `sampling` limit | a model limit only narrows |
| who may call the model, whether it is routed | `model_access`, `rotation_models_json`, escrows | filters never open or route a model; a filter refusal (for example `tools: reject`) is answered before the access check, as every filter refusal is |
| context length | the chain's `--max-model-len`, else `model_limits.max_model_len` | filters do not read it; a host refuses an over-long request and the engine retries elsewhere |
| streaming upstream, logprobs handling | `GATEWAY_FORCE_UPSTREAM_STREAMING`, `GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE` and their settings keys | independent of filters |

A filter entry lists `warnings` when the settings' limits override its filters — a cap below the floor, a cap below the budget's `force_zero_below`, or a default budget below it — so the conflict is visible where the filters are edited. Neither PUT refuses it: the two are edited separately and either order must be possible.

A cached reply is keyed on the normalized body, the model and the empty-answer exemption, so a filter change never serves a reply produced under the old filters; entries also expire within the cache TTL.

### When a release changes the presets

Presets are code: a release can change them, and the gateway applies the change at boot.

- **A model on its built-in preset** runs the new values at once.
- **A stored setting** keeps only its preset name and the knobs it sets. At boot it is recomposed over the release's preset, so it follows every value it does not set itself and keeps every value it does. A setting that named no preset follows the model's current built-in preset; if a release rebinds the model, the setting's base moves with it.
- **A stored setting that no longer reads** — its preset was renamed or removed, a knob it sets was renamed, or a new rule refuses its combination — is not applied. The gateway logs a warning at boot, the model runs its built-in preset, and the entry shows `source: unreadable` with the reason. Re-PUT the setting in the new form or DELETE it.
- **A preset file the release cannot load** stops the release at its own tests; it never reaches a gateway.

To pin a model to exact values across releases, PUT every knob that matters to you; a preset you name only sets the values you leave out.

### A new version of a model

The chain names a new version with a new model id. Every setting is keyed by the exact id — filter settings, built-in bindings, `model_limits`, `model_access` — so nothing carries over: the new id runs the `default` preset and is admin-only until it is configured. Configure it with one PUT that names the preset of the version it replaces, and give it its `model_limits` and `model_access` in `/v1/admin/settings`.

### Who may call what

`api/routes.go`, `authorizeModel`. Three tiers per model, from `limits.model_access`, which the admin override `model_access` sets:

| Tier | Who |
| --- | --- |
| `open` | anyone |
| `api_key` | a caller presenting one of `DEVSHARD_API_KEYS` |
| `admin_only` | a caller presenting `DEVSHARD_ADMIN_API_KEY` |

**Two behaviours to know before editing it:**

- A model **not listed** in `model_access` is `admin_only`, an empty map included. A deployment starts with no entries, so until the operator names a model `open` or `api_key` only the admin reaches it, and setting `DEVSHARD_API_KEYS` alone opens nothing. While any model is `open` the gateway logs a warning naming it, at startup and after every admin override, since an open model spends the operator's escrows for any caller.
- With `DEVSHARD_ADMIN_API_KEY` unset the whole admin surface answers **404, not 401** — the routes do not exist rather than rejecting the caller. A "route not found" on `/v1/admin/…` usually means the key is missing, not the path. Without it no one can set `model_access` either, so every model stays admin-only and the gateway serves no client.

An admin key satisfies every tier, so admin calls never need a second key. A `PUT` of the settings replaces the whole override set, so a `PUT` that leaves out `model_access` closes every model to clients again; a `POST` changes only the fields it names ("Configuration").

`limits.model_access`, together with `limits.model_limits`, is also the list of models the gateway offers (`config.Limits.Offers`): a model named in either one answers 503 when nothing currently routes to it, and a model named in neither answers 400 while any model is routable — an empty registry answers 503 for every model instead.

### The kill switch

`modes.disabled` (env `DEVSHARD_GATEWAY_DISABLED`, or `disabled` through the admin settings endpoint) stops serving clients while leaving `/metrics`, `/healthz`, `/v1/status`, the admin surface and the recovery surface up — so a gateway can be taken out of service and still be inspected, settled and drained.

With `disabled_redirect_url` set it answers **308** with the new URL in both the header and the body; without one, **503** with `disabled_message`. The distinction matters to clients: a 308 is a permanent move, a 503 is "come back later".

## Configuration

Three layers, later wins:

1. **Defaults** — `config/defaults.go`, `Defaults()`. The only place a default lives.
2. **Environment** — read once at boot, in `env/` and nowhere else. `env.Load` returns *what is set* (a nil pointer is unset), so an unset variable can never overwrite a default with a zero.
3. **Admin overrides** — the fields of `config.Overrides`, written through `/v1/admin/settings`, persisted in the store and reloaded at boot. A `POST` is a patch over the stored overrides (`Overrides.Merged`): a field it names replaces the stored one, a field it sets to `null` is cleared back to the layers below, and every other field stays. A `PUT` is the whole document and replaces the stored one. Writes are applied one at a time, so two operators patching at once cannot lose each other's fields. They take effect without a restart: the config is an immutable snapshot swapped whole, and every reader loads it per request. The `settings written` audit line carries the method, the open models, the kill switch and both routing lists as they stand after the call.

Parse failures are accumulated, so a boot reports **every** misconfigured variable at once rather than one per restart.

### Variable names

Each variable is read under one name only. `DEVSHARD_CHAIN_GRPC` and `DEVSHARD_CHAIN_RPC` fall back to `NODE_GRPC_URL` and `NODE_RPC_URL`. Two grammars apply. The lenient one covers every `DEVSHARD_*` variable and six `GATEWAY_*` limits — `GATEWAY_DEFAULT_MAX_TOKENS`, `GATEWAY_MAX_TOKENS_CAP`, `GATEWAY_MAX_CONCURRENT_REQUESTS`, the two `_PER_10000_WEIGHT` limits and `GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT`. It reads booleans in devshard's shared grammar (`on`, `off`, `yes`, `no` as well as `true` and `false`), `DEVSHARD_STATS_TOKENIZER` among them, except the three height-sync switches, which keep their own spellings: `DEVSHARD_GATEWAY_CHAIN_ORACLE` and `DEVSHARD_HEIGHTSYNC_HEARTBEAT` are on only for `true`, `1` or `on`, and `DEVSHARD_REQUIRE_HEIGHT_SEED` is off only for `0`, `false`, `off` or `no`; `DEVSHARD_GATEWAY_HOST_PING_INTERVAL`, `_TIMEOUT` and `DEVSHARD_QUIET_SETTLE_DELAY` as durations (`15s`); `DEVSHARD_POC_REQUEST_MODE` in any letter case. A lenient value that is unparsable, negative, or zero where zero means the default (`GATEWAY_DEFAULT_MAX_TOKENS`, `GATEWAY_MAX_TOKENS_CAP`, the two `_PER_10000_WEIGHT` limits, the transaction, cache, port and snapshot settings, `DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS`, `DEVSHARD_MAX_CONCURRENT_PROMPT_COUNTS` and `DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY`) is ignored with a warning naming the variable, so the default applies. A `GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT` set alone also sets the PoC limit unless it equals the default. Every other `GATEWAY_*` variable is strict and refuses a bad value at boot. An **empty** value counts as unset.

Four defaults shape a first deployment: the nonce ledger (`DEVSHARD_STATS_ENABLED`) is off, `DEVSHARD_POC_REQUEST_MODE` is `relaxed`, `DEVSHARD_LOG_FORMAT` is `json`, and `GATEWAY_MAX_CONCURRENT_REQUESTS` is 2048. These variables are not read, and setting one has no effect:

- `DEVSHARD_CAPACITY_AWARE_LIMITS` — the weight model is always on;
- `DEVSHARD_ROUTE_PREFIX` — a prefix is set per escrow, through `route_prefix` in `DEVSHARDS_JSON`;
- `DEVSHARD_ESCROW_ID`, `DEVSHARD_MODEL`, `DEVSHARD_STORAGE_PATH` and a raw `DEVSHARD_PRIVATE_KEY` — escrows come from `DEVSHARDS_JSON`, whose entries also need `model` and `private_key_env`;
- `LOG_FORMAT` (only `DEVSHARD_LOG_FORMAT` is), `DEVSHARD_LOG_LEVEL`, `DEVSHARD_HEIGHTSYNC_PROBE_INTERVAL`, `DEVSHARD_GATEWAY_HEIGHTSYNC_PEER_MATRIX`, `DEVSHARD_META_DRAIN_TIMEOUT_SECONDS`, `DEVSHARD_CAPTURE_SHORT_CONTENT_*`;
- `GATEWAY_AGGREGATE_*`, `GATEWAY_CLASSIFY_*` and `GATEWAY_ERROR_STREAM_MAX_ATTEMPT_BYTES` — buffered replies are bounded by `GATEWAY_MAX_BUFFERED_RESPONSE_BYTES`.

`DEVSHARD_PUBLIC_API=none` is refused: the phase observer needs the public API.

Signing keys are addressed **by the name of the variable that holds them**, never by value: `escrows_json` and `rotation.models_json` carry `private_key_env`. Log lines and errors name the variable, never the key.

### The knobs that decide behaviour

| Variable | Default | What it decides |
| --- | --- | --- |
| `DEVSHARD_PORT` | 8080 | the listening port |
| `DEVSHARD_CHAIN_ID` | empty | the chain id the gateway signs its transactions for; empty asks the node over `DEVSHARD_CHAIN_GRPC`, and a configured value wins over the node |
| `DEVSHARD_STORAGE_DIR` | `$HOME/.cache/gonka-gateway` | where `gateway.db` and the escrow storage live |
| `DEVSHARD_SESSION_RETENTION_EPOCHS` | 2 | how many epochs before the effective one an escrow's session directory stays on disk; `0` keeps every one, and `1` is raised to 2, the hosts' own horizon ("Session storage" below) |
| `GATEWAY_MAX_CONCURRENT_REQUESTS` | 2048 | the hard admission ceiling; `0` sets no static cap and lets the weight model decide alone |
| `GATEWAY_ADMISSION_QUEUE_WAIT_MS` | 300000 | how long a request waits for a slot before 429 |
| `GATEWAY_ADMISSION_QUEUE_PER_SLOT` | 4 | how deep the queue is allowed to grow per slot |
| `GATEWAY_MAX_BUFFERED_RESPONSE_BYTES` | 512 MiB | **every** non-streaming reply being assembled, at once |
| `DEVSHARD_CHAT_CACHE_MAX_BYTES` | 256 MiB | the response cache |
| `GATEWAY_DEFAULT_MAX_TOKENS` / `GATEWAY_MAX_TOKENS_CAP` | from `filters` | the output budget a request gets and may ask for |
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
| `GATEWAY_ENGINE_MAX_CONCURRENT_TIMEOUT_VOTES` | 64 | chain votes for unfinished nonces being posted at once, not a bound on settling escrows; one past it waits for a poster rather than being dropped |
| `DEVSHARD_STATS_ENABLED` | false | the per-nonce ledger and its JSON API on `DEVSHARD_STATS_PORT` (9091) |
| `DEVSHARD_STATS_SNAPSHOT_SECONDS` | 60 | how often the nonce ledger writes its snapshot to disk, and so the most a crash can roll it back; a value below 1 is ignored with a warning |
| `DEVSHARD_STATS_RETENTION_EPOCHS` | 2 | how many epochs before the current one the nonce ledger keeps retired escrows; `0` keeps every epoch |
| `GATEWAY_PERF_EWMA_HALFLIFE_SECONDS` | 600 | how fast a host's history forgets |
| `GATEWAY_TIMEOUT_SWEEP_BUDGET_PER_TICK` | 8 | execution-timeout votes one tick may retry across every escrow, the most overdue first; `0` turns the sweep off |
| `GATEWAY_TIMEOUT_SWEEP_GRACE_SECONDS` | 120 | how far past its deadline a nonce must be before the sweep claims it from its own race |
| `DEVSHARD_POC_REQUEST_MODE` | relaxed | `relaxed` keeps serving through proof-of-compute; `off` refuses new requests while the chain blocks them |
| `GATEWAY_HEIGHT_SYNC_ENABLED` | true | whether the escrow's heartbeat cadence runs. Turn it off for a fleet whose hosts do not carry height sync, or every interval logs a heartbeat it could not stamp ([`docs/escrows.md`](escrows.md), "Height sync") |
| `DEVSHARD_HEIGHTSYNC_HEARTBEAT` | false | whether a quiet escrow composes heartbeat turns; on only for true/1/on. A finished request's leftovers settle either way ([`docs/escrows.md`](escrows.md), "Height sync") |
| `DEVSHARD_REQUIRE_HEIGHT_SEED` | true | whether a session chases a host-signed tip from the moment it opens and reports `seed_incomplete` when it cannot. It does not refuse requests — see [`docs/escrows.md`](escrows.md), "Height sync". Off in the e2e stand |
| `DEVSHARD_HEIGHTSYNC_K` / `DEVSHARD_HEIGHTSYNC_SLOTS` | 10 / 1 | the anchor cadence carried on the height-sync envelope |
| `DEVSHARD_QUIET_SETTLE_DELAY` | 5m | how long a session keeps a finished request's leftovers for the next request before composing them on its own nonce; devshardctl reads the same variable, and the test stand sets `5s` |
| `DEVSHARD_GATEWAY_CHAIN_ORACLE` | false | whether the gateway follows mainnet itself, from `NODE_MANAGER_ADDR`, the chain RPC and gRPC. It is not what the gateway stamps from — it only labels how much a carried tip is trusted ([`heights/README.md`](../heights/README.md)) |
| `DEVSHARD_NODE_MANAGER_ADDR` / `NODE_MANAGER_ADDR` | localhost:9400 | node-manager's address, resolved once (`runtimeparams.SettingsFromEnv`) for both its readers: the runtime-params feed and, when it is on, the height follower. Unreachable is not fatal to either — the feed polls the chain instead, less promptly, and the follower fails over to the chain RPC and gRPC |
| `DEVSHARD_PARAMS_SOURCE` | auto | which feed answers for governance: `auto` prefers the long poll with a chain fallback, `chain` polls the chain alone and dials no node manager |
| `DEVSHARD_GATEWAY_HOST_PING_DISABLED` | false | whether the hosts the live escrows use are pinged for reachability and clock drift. Observability only — a host that stops answering a ping keeps its routing weight ([`hostping/README.md`](../hostping/README.md)) |
| `DEVSHARD_GATEWAY_HOST_PING_INTERVAL` / `DEVSHARD_GATEWAY_HOST_PING_TIMEOUT` | 15s / 2s | the probe cadence and its patience; the timeout must be at most half the interval or the ping is refused and switched off |
| `DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY` | 8 | hosts probed at once within one wave |
| `DEVSHARD_ALLOW_PRIVATE_ADDRESSES` | false | whether dials to private addresses are allowed. A host URL comes from chain state, so the guard is on in production and only a stand whose hosts are Docker names turns it off; turning it off is logged |
| `DEVSHARD_OTEL_ENABLED` | false | whether OpenTelemetry traces are exported; off is a no-op tracer ("Tracing" below) |
| `OTEL_ENDPOINT` / `OTEL_HEADERS` | unset | the OTLP gRPC collector and its headers as `key=value,key=value`; a malformed header is skipped with a warning |
| `DEVSHARD_LOG_FORMAT` | json | one JSON object per line, which promtail and the Loki panels read; any other value is the text form |

### The rest of the variables

These are read the same way and rarely need changing. Where a knob has a longer explanation, the last column points at it.

| Variable | Default | What it decides |
| --- | --- | --- |
| `DEVSHARD_GATEWAY_DISABLED` | false | the kill switch ("The kill switch" above) |
| `DEVSHARD_GATEWAY_DISABLED_MESSAGE` | empty | the text of the 503 a disabled gateway answers ("The kill switch" above) |
| `DEVSHARD_ENABLE_CONTENTION_PROFILES` | false | whether lock contention and blocking are sampled, so `/debug/pprof/mutex` and `/debug/pprof/block` hold data; it costs a little on every contended lock, so it stays off outside a diagnosis, and turning it on is logged |
| `DEVSHARD_GATEWAY_DISABLED_NEW_URL` | empty | the URL a disabled gateway redirects to with 308; empty means 503 |
| `DEVSHARD_API_KEYS` / `DEVSHARD_ADMIN_API_KEY` | empty / empty | the client keys an `api_key` model accepts and the admin key; an admin key shorter than 16 characters is refused, and an empty one leaves the admin surface unrouted ("Who may call what" above) |
| `DEVSHARDS_JSON` | empty | the escrows seeded into the store at boot, each naming its `id` (the chain escrow id in canonical decimal; any other fails the boot), `model` and `private_key_env` ("Boot" below) |
| `DEVSHARD_CHAIN_GRPC` / `DEVSHARD_CHAIN_RPC` | `localhost:9090` / empty | the chain's gRPC endpoint, and the CometBFT RPC its queries fall back to; an empty RPC endpoint is derived from the gRPC host at the standard port ([`README.md`](../README.md), "Escrow sessions and the chain connection") |
| `DEVSHARD_PUBLIC_API` | `http://localhost:9000` | the public API the phase observer polls; it must be an absolute URL |
| `DEVSHARD_RPC_H2_UPGRADE` | on | whether a session's connection to a host first tries HTTP/2 on the origin of the host's inference URL (h2c on `http`, TLS offering ALPN `gonka-rpc/1` on `https`) and drops to Connect over HTTP/1.1 on the same origin when the host does not speak it; `0`, `false`, `no` or `off` stays on HTTP/1.1 |
| `DEVSHARD_RPC_GRPC` | off | whether that HTTP/2 connection speaks native gRPC instead of Connect; on only for `1`, `true`, `yes` or `on`, and never used over HTTP/1.1 |
| `DEVSHARD_RPC_MAX_CONNS_PER_PEER` | 2 561 | the most HTTP connections the gateway keeps open to one host; a value that is not a positive integer falls back to the default without a warning |
| `DEVSHARD_RPC_ENDPOINTS` | unset | not honoured: every peer method uses Connect, and a set value is logged once as ignored |
| `DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS` | 16 | escrow sessions opened at once while the boot publishes the active escrows ("Boot" below) |
| `DEVSHARD_MAX_CONCURRENT_PROMPT_COUNTS` | half of `GOMAXPROCS`, at least 1 | prompts tokenized at once after the cache lookup; a request that finds every slot taken is not held but carries the byte estimate instead ([`request.md`](request.md), "The prompt token count") |
| `DEVSHARD_STATS_TOKENIZER` | off | whether every request that admission lets through and the cache does not answer is counted with the model's tokenizer after the cache lookup, through the gate above, with each pre-token's merge capped at 512 bytes; off, only a prompt the context check tokenizes carries a tokenizer count and every other request the byte estimate ([`request.md`](request.md), "The prompt token count") |
| `DEVSHARD_TX_FEE_DENOM` / `DEVSHARD_TX_FEE_AMOUNT` | `ngonka` / 1 000 000 | the fee every chain transaction of the gateway carries; a fee in `ngonka` is added to what an escrow create must find in the wallet |
| `DEVSHARD_TX_GAS_LIMIT` | 700 000 | the gas limit of those transactions |
| `DEVSHARD_TX_POLL_INTERVAL_MS` / `DEVSHARD_TX_POLL_TIMEOUT_MS` | 2 000 / 45 000 | how often a broadcast transaction is looked up, and how long before the gateway stops waiting for it |
| `GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT` | 5 | the requests a host's weight buys it, and with them the fleet's own ceiling ([`capacity.md`](capacity.md)) |
| `GATEWAY_POC_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT` | 10 | the same figure while the chain reports requests blocked for proof-of-compute |
| `GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT` | 0 | the per-model budget of input tokens in flight, scaled by capacity, in tokenizer tokens for a request counted with the model's tokenizer and in byte-estimate tokens otherwise ([`request.md`](request.md), "The prompt token count"); `0` is unlimited |
| `GATEWAY_MATCH_WAIT_MS` | 2 000 | how long a bound nonce waits for a compatible request before it is burned; `0` burns at once, and 5 000 is the most it accepts |
| `GATEWAY_MAX_CONSECUTIVE_BURNS` | 2 | nonces one escrow may burn in a row before the next one is sent over a full congestion window; `0` never forces a send ([`routing.md`](routing.md), "The forced send") |
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
| `GATEWAY_PERF_EJECTION_BASE_SECONDS` / `GATEWAY_PERF_EJECTION_MAX_SECONDS` | 30 / 600 | how long an ejection lasts, the first time and at most |
| `GATEWAY_PERF_MAX_EJECTION_FRACTION` / `GATEWAY_PERF_MIN_AVAILABLE_HOSTS` | 0.5 / 4 | the share of a model's hosts that may be ejected at once, and the number that always stays routable |
| `GATEWAY_PERF_HOST_STALENESS_SECONDS` | 3 600 | how long an unseen host is remembered before its history and its participant-labelled metric series are dropped |

`env/env.go` remains the authoritative list and `config.Defaults()` the authoritative set of defaults; a variable added there belongs in one of the two tables above. Three sets sit outside `env/env.go`, because they are the same knobs every devshard binary honours: the runtime-params feed reads the fleet-shared `DEVSHARD_*` / `DEVSHARDD_*` knobs declared in `devshard/runtimeparams/env.go`, a session's connection to its hosts reads the four `DEVSHARD_RPC_*` knobs above, declared in `devshard/transport`, and tracing reads `DEVSHARD_OTEL_ENABLED` in `devshard/observability` and `OTEL_ENDPOINT` / `OTEL_HEADERS` in `common/observability`.

## Boot

`app/lifecycle.go`, `bootOrder` and `startAll`. The order is a declared list rather than a sequence of calls, so a test can read it back (`app/boot_order_test.go`), the way the shutdown order is. It is load-bearing:

1. the chain observer starts — nothing downstream can score a host before a snapshot exists;
2. the warmup prober starts;
3. the host pings start — observability alone, and they read the live set per wave, so starting before anything is published costs nothing ([`hostping/README.md`](../hostping/README.md));
4. `seedDevshards` applies `escrows_json` to the store — a seeded escrow that names no key variable is **refused**, not silently accepted. It leaves a row it already holds alone, but settlement deletes the row of an escrow it settled, so on every later boot the seed writes that escrow back as active;
5. `publishEscrows` opens a session per active escrow and publishes it for routing, bounded by `MaxConcurrentRuntimeBuilds` (16) so a large set does not open 200 sessions at once. An escrow the chain no longer has, or whose key is missing, is marked inactive; one the chain reports settled is refused before its session opens (`bridge.ErrEscrowSettled`) and its row is deleted, as settlement deletes it, so a seed written back by step 4 is never published or routed; one whose session would not open for any other reason stays unpublished, is logged as `devshard could not be opened, retrying`, and is retried every lifecycle tick (15 s) until it opens, so one escrow never keeps the others from serving. A group member the chain no longer lists does not stop a session opening: its slot fails every call as a down host's would (`user.ErrHostAbsentFromChain`) until the session is reopened, which step 8 does once the chain lists the participant again;
6. the nonce ledger starts, so nothing above it files a fact into a sink that is not yet reading;
7. the escrow lifecycle manager starts its 15 s tick;
8. the store's write notifications start republishing escrows on change, and on a timer while step 5 left an escrow unopened; the same loop probes the chain for participants absent when an escrow's session opened, and reopens that escrow once one is listed again, all of a model's such escrows in the same probe while another escrow of the model with no dead slot serves it ([routing.md](./routing.md), "A slot the chain did not list at open");
9. the HTTP listener opens — **last**, so the first request meets a gateway that is fully assembled.

`startAll` stops at the first step that fails and names it, and `serve` shuts down cleanly rather than serving half-built. Steps 4 and 5 are the only two that can fail: the rest start a goroutine or a listener and return. Step 5 fails only when the store cannot be read or a row cannot be retired or marked inactive, not when an escrow's session will not open.

## Shutdown

`app/lifecycle.go`, `serve` and `shutdownOrder`. Before the first step, `serve` cancels the background context the chain observer, warmup prober, host pings, nonce ledger, escrow lifecycle and devshard-write republish were started under (`stopThenShutDown`), after a boot that failed as after a signal. Then thirteen steps, in this order, bounded by the grace period, with up to one more second from the journal step's floor:

| # | Step | Why here |
| --- | --- | --- |
| 1 | http server | stop taking new work first; open requests get the grace period less the reserve the steps below keep (`laterStepsReserve`, 5 s) and less the cancelled wait to finish uncancelled (`httpGracefulShare`, 4 s of the 10 s), so a short answer or an operator call still running completes; only if the server is not idle by then is every request still open cancelled, and the step waits at most 1 s more for those handlers (`httpCancelledWait`), so a stream that would never end holds the step for 5 s at most and the steps below keep their reserve; a race whose client is cancelled this way drains in step 2 like any client departure |
| 2 | races | in-flight requests finish, and the losers post the votes they owe if those fall due inside the grace period (10 s); a refused nonce's vote falls due about 65 s after its refusal, so one refused in the last minute before a restart is not voted and finalize pays it in full |
| 3 | dispatchers | nothing new reaches a host |
| 4 | escrow lifecycle | no rotation starts mid-drain |
| 5 | chain observer | nothing above still needs a snapshot |
| 6 | nonce sweep | the nonce ledger's sweep stops and the step waits for the sweep in progress, so no sweep reads a closing session or retires an escrow once the live set is emptied |
| 7 | escrow sessions | **destroys state** the steps above may still use; the nonce ledger takes its last reading of each escrow just before its session closes |
| 8 | journal | every producer with a shutdown step above has stopped; it drains its queue into the ledger below, waiting for whatever budget remains, or for one second when the steps above leave less, and counts anything later as a late event, such as a line from the warmup, which is only cancelled, or from the republish after a devshard write |
| 9 | nonce accounting | after every emitter, so the final snapshot holds the counters the run ended with |
| 10 | runtime params | after the sessions whose schedule it answers for. It cancels the feed and closes the node-manager connection without waiting for an in-flight poll; nothing above it still reads a schedule by then |
| 11 | height follower | after the sessions that carried its readings; it holds a block subscription and a dialled client |
| 12 | store | every step above may still write to it |
| 13 | public API connections | every step above can still reach it; closing earlier just forces a re-dial |

`stopAll` runs every step **even after one fails**, except a step marked `needsQuiesced` (step 7): if anything above it failed, work may still be running, so closing the sessions would pull storage out from under it. That step is skipped and the skip is reported. A stream cancelled in step 1 hands its race off to keep running and step 2 waits for it, so a host still generating when the grace period left to step 2 runs out makes that drain overrun and step 7 is skipped (`TestShutdownSkipsEscrowSessionsWhenADrainOverran`).

Each drain is bounded by the grace period but **not cancelled** by it — a step that runs out of time is reported as "abandoned with work still running" rather than killed mid-vote.

The `journal` step (8) is bounded the same way, but with a floor: it waits for its queue for whatever budget remains, or for one second whenever the steps above it leave less than one second of the grace period, so its queue can still reach the ledger, and a close that outlasts that wait is reported as "abandoned with events still queued".

**Docker's stop timeout.** Docker sends SIGKILL ten seconds after SIGTERM unless the service sets `stop_grace_period`, and ten seconds is the grace period itself, so a gateway with a stream still open would be killed before the journal drains, the final accounting snapshot is written and the store closes. Both compose files that run the gateway (`deploy/docker-compose.devshard-gateway.yml` and the join stack's `deploy/join/docker-compose.devshard-gateway.yml`) set `stop_grace_period: 30s`, which covers the grace period, the journal's one-second floor and the steps after it. A deployment that raises the grace period raises this with it.

## Session storage

Each escrow keeps its session in `<storage dir>/escrow-<id>/epoch_<N>.db`, where N is the epoch the chain created the escrow in. On a production gateway a completed epoch leaves 5–7 GB of these behind, so the gateway removes them once the hosts have (`app/session_retention.go`). Hosts keep the current epoch and the two before it (`storage.DefaultEpochRetain`), and the chain settles an escrow only in its own epoch or the next (`MsgSettleDevshardEscrow` refuses any later one), so a session past that horizon can neither serve nor settle. `hostHorizonPreviousEpochs` is that horizon less the current epoch (`storage.DefaultEpochRetain - 1`): a session younger than it can still settle.

On the first chain snapshot and whenever the effective epoch moves forward, one background pass removes every `escrow-<id>` directory whose `epoch_<N>.db` is more than `DEVSHARD_SESSION_RETENTION_EPOCHS` epochs before the effective one. The effective epoch trails the latest by one during PoC, so a pass never runs ahead of the hosts. The pass leaves alone a directory with no `epoch_<N>.db` (a session still opening), an escrow the registry still holds, and one whose row is active or carries a settle transaction still being reconciled. A parked escrow is removed like any other: by then the chain refuses its settlement, and with settlement disabled nothing would ever clear the row. A directory the pass cannot read or remove is logged (`session storage left in place`) and skipped, so it does not hold back the rest; it is tried again next epoch. A pass that cannot list the rows or the storage dir, or that runs past a minute, is logged and runs again on the next snapshot, picking up where it stopped. The pass stops with the gateway. A value below the hosts' horizon is raised to it with a warning; `0` turns the pass off.

## Logs

The gateway writes a line for every event that **moves money, changes what it will serve, or is an operator's own doing** — and for very little else. Failures on the money path are not logged separately: each is returned as an error naming its own step (`resolving signer for escrow X`, `building settlement for escrow X`) and the escrow tick logs the joined result once as `escrow tick failed`. A busy or in-flight settle is deferred to a later tick, not a failure, and is not part of it. A success has no such carrier, which is why the successful transitions are the ones written down.

Lines are JSON objects by default. Promtail lifts `level` into a Loki label (`deploy/join/observability/promtail-config.yaml`), and the Loki panels parse the rest with `| json`, so a gateway switched to `DEVSHARD_LOG_FORMAT=text` empties those panels.

Every lifecycle line is written by the journal (`journal/`) in the order its steps happened. A file named beside a line below is the step's producer, unless it names a `journal/render_*.go` renderer or says "written by". A test fails on a lifecycle line written around the journal and on a key the log vocabulary does not declare (`journal/guard_test.go`, `journal/keys_test.go`).

### The trace

Always on, with no level knob — a trace that ships off by default is not there for the incident that already happened, and switching it on afterwards cannot recover what was not written. Roughly five lines per request.

| Line | Carries |
| --- | --- |
| `nonce committed` (emitted by `engine/pick.go`, written by `journal/render_race.go`) | request, escrow, nonce, participant, slot, role, and why this attempt started |
| `attempt finished` (emitted by `engine/report.go`, written by `journal/render_race.go`) | the same identity, the terminal verdict, whether the nonce was finished, whether the host diverged on state, and each deadline its host was narrowed for (`missed_receipt_deadline`, `missed_first_token_deadline`) |
| `served winner finish landed` (emitted by `engine/winner_sample.go`, written by `journal/render_race.go`) | request, escrow, nonce and participant of a winner whose nonce was still open when its stream ended, once its Finish is seen, with `finish_lag_ms` from the attempt's end ([race.md](./race.md), "A served winner's sample waits for its finish") |
| `nonce stranded` (emitted by `engine/race.go`, written by `journal/render_race.go`) | **Warn** — a committed nonce nobody will answer for; the shape every recurring settlement defect takes |

The journal's consumer writes these lines, so under load, or while the nonce ledger copies itself for a snapshot, it can drop `nonce committed`, `attempt finished` and `served winner finish landed` — counted in `devshard_gateway_journal_progress_dropped_total` and announced by `journal skipped progress lines` — while `nonce stranded` is refused only past the money ceiling.

A line the journal writes is stamped when the journal's consumer writes it, not when the event happened, so it can trail — for example, while the consumer waits for the nonce ledger's lock as `Book.Snapshot` copies the ledger. Journal lines keep their order among themselves. Lines written directly — the shared `devshard/user` session lines and the gateway's process, admin and store lines — are stamped when they happen, so the two can interleave out of order. `duration_ms` and the other `*_ms` fields are measured at the event, and are the timings to trust.

Follow one request by grepping its request id; follow one nonce through commit, dispatch and verdict by grepping the nonce.

`attempt finished` carries the coordinator's reading of the attempt at the moment it completed: `racedTerminal` (`engine/report.go`) makes a backstopped attempt read `hard_timeout`, a stalled one `stalled`, an answer its host closed with neither `[DONE]` nor a finished nonce — a loser's as well as the crowned one's — `sse_truncated`, and the winner `won`. Only a later `abandonedByHosts` reclassification, made once the whole race has finished, reaches the ledger without reaching this line.

### When a host stops taking work

Three mechanisms withhold work from a host, each on its own trigger, and each is a gauge in Prometheus. A gauge is sampled every 15 or 30 seconds while the first rung of two of them lasts 30 seconds and 5 seconds, so the shortest withholdings pass entirely between two scrapes. Each producer therefore also narrates each edge through the journal, which writes one line for it, and nothing in between: the volume follows the number of hosts and their own windows, never the request rate.

| Line | Trigger | Carries |
| --- | --- | --- |
| `host withheld from routing` (`perf/tracker.go`) | five failures in a row, or a failure rate from 15% over a volume from 20 | **Warn** — which trigger fired, the rung, the run length, the rate and its volume that tripped it, and how long the withholding lasts |
| `host back in routing` (`perf/tracker.go`) | first sample after the withholding lapsed | the rung it decayed to |
| `host cut off after transport faults` (`limits/reaction.go`) | three cut-off faults in a row — a transport fault, an empty answer that left its nonce open, or a `429`/`503` from a host that carried nothing else of ours for this model when it refused — or one failed half-open probe | **Warn** — which of the two, the backoff depth, and how long the cut-off lasts. The line's name and its `consecutive_transport_faults` reason are what dashboards and log queries match on, so both keep the wording they have whatever the run was made of |
| `host back after its cut-off` (`limits/reaction.go`) | the probe answered | the backoff depth it decayed to |
| `host denied the crown` (`engine/crown_strikes.go`) | three content-free answers in a row | **Warn** — the strike count. The host keeps drawing nonces and starts a second attempt beside itself, so this is a spend, not only a quality signal |
| `host crowned again` (`engine/crown_strikes.go`) | one answer with content | — |

One more line belongs to the same family, on the money side rather than the routing one: `execution timeouts swept` (`escrow/manager.go`), written by the escrow tick only when the sweep found nonces to re-vote, carrying how many were due, applied and failed. Silence means nothing was owed.

### The request record

`request finished` (`journal/render_request.go`), one line per completed race: Info when it went out clean, Warn when it did not. It answers what a finished request can no longer be asked:

| Field | What it settles |
| --- | --- |
| `escrow` | the escrow the race ran on; absent when no escrow was picked |
| `host` / `hosts` | `host` is the crowned attempt's host; with nobody crowned, `hosts` lists every host tried, comma-separated; a race that ran no attempt carries neither |
| `bytes` | how much actually reached the client, counted at the socket — the strip rewrites events on the way out |
| `terminated` | whether the SSE terminator went with them; without it a client waits out its own timeout on a reply it already has |
| `outcome` | `served`, `failed_mid_stream`, or `failed_before_first_byte` — the last distinguishes a reply the client can retry from one it cannot |
| `deliver_error` | the failure that reached the client instead of the last bytes: the case no status code can express, because a stream commits 200 on its first byte |
| `nonce_finished` | whether the crowned attempt's nonce closed on the chain. A served request with `false` here answered the client and left the escrow owing a vote for that nonce. Absent when nobody was crowned |

The record carries no request or response body — capture files exist for that, sampled and bounded. Its `error` field is truncated at 256 bytes, and that is not tidiness: a host error with no message renders its raw upstream payload as the error text, so an untruncated field would write a whole SSE event, generated tokens included, once per failed request. The cap is `maxLoggedErrorBytes`, and the renderer reserves `loggedFieldSlots` keyvals up front, every one a finished request can carry, so the line is never copied to grow (`journal/render_request.go`).

`gateway limiter turned a request away` sits on the journal's progress lane rather than this line's money lane, so it can be skipped when the progress backlog is full — the exact condition a refusal storm creates. A skipped line is counted in `devshard_gateway_journal_progress_dropped_total`; every refusal, logged or not, is still counted in `devshard_gateway_limit_rejections_total`.

### Lines that mean something happened

| Line | Why it matters |
| --- | --- |
| `escrow created` / `escrow settled` | the moments funds were committed and left; the settle line is the audit record |
| `settled escrow record dropped` | that row named the only key able to settle the escrow — removal is irreversible |
| `session storage removed past the host horizon` / `session storage left in place` / `session retention pass failed` / `session retention raised to the host horizon` | `removed` escrow session directories left the disk for good; one directory could not be read or removed and is tried again next epoch; a pass that failed runs again on the next snapshot; a configured `DEVSHARD_SESSION_RETENTION_EPOCHS` below the hosts' horizon was raised to it ("Session storage" above) |
| `escrow recovered from commitment` | a create landed while the gateway was down, so `escrow created` never ran; the escrow exists in no other line |
| `commitment cleared` | a creation intent was abandoned, with the reason — one of which (`transaction created no escrow`) means the transaction *did* commit |
| `escrow gone from chain, taken out of service` | `escrow retired` also fires for settlement parking, so this is the only line carrying the cause; it also fires for a row already out of service whose escrow the chain pruned past its settle deadline, which leaves the unsettled count with it |
| `escrow chain epoch and amount unresolved` | the row's `chain_epoch` and `amount` could not be read, with the reason; written once per row until it resolves, so the row's `chain_epoch: 0` in the admin JSON is the only sign it is still unresolved. A row out of service past its settle deadline whose escrow the chain pruned is marked gone from chain, and leaves this line for good |
| `escrow marked for replacement` | **Warn** — an escrow reached its nonce cap and is marked for the next escrow tick, with the `reason`; written once per escrow. A balance mark writes no line, since nothing replaces it: the funding planner's lines below say what happens to the money |
| `regular escrows promoted to temp` | the bridge funded fewer temps than `temp_count` — no room under `max_unsettled`, a model the network does not serve, or a failed create — so the regulars keep serving as temps instead of being retired; `rotation_status.create_error` names the cause (`bridge funded fewer temps than temp_count` for a partial fill) |
| `escrow parked at its settlement deadline` / `escrow deadline passes unsettled` / `settle margin shorter than two settle windows` / `escrow deadlines read past a stale chain height` | an escrow came within `rotation.settle_margin_blocks` of `settle_by`, the switch block by which its chain epoch must settle, and was parked; a row inside its margin that nothing is allowed to settle, or whose deadline has passed (Error, with `reason`: `settlement_disabled`, `operator_deactivated`, `key_missing`, `deadline_passed`; once per row while it lasts; `settle_by` is left out once the switch block is no longer known, as on `deadline_passed`), a margin shorter than two settle windows at the measured block time, and a chain snapshot older than `chain_snapshot_max_age_seconds` whose deadlines are read at `projected_height` (`height` plus `stale_for_ms` over the measured block time, or one block a second), once per episode |
| `escrow funding planned` / `funding guarantee broken` / `model amount cannot fund a full escrow` / `escrow budget reached` | the funding planner's decision for a model and the start of each episode. Progress lane, so a backlog may drop them |
| `nonce burned for nobody` (`journal/render_money.go`) | a committed nonce that will serve nobody, with the escrow and the reason, and under `burned_during_request` the request it was spent during |
| `a host stopped mid-answer: reply served, not cached` (`journal/render_request.go`) | a reply served as an answer never reached a terminal `finish_reason`, so nothing replays it. An answer whose host closed the stream with neither `[DONE]` nor a finished nonce never gets here: it is a failed request, a 502 with an error body when buffered and an error event with no `[DONE]` when streamed (`engine/failure.go`, `ErrAnswerTruncated`). This line names the rest, a reply the host terminated or finished without a terminal reason, and only one the cache would otherwise have stored: with `chat_cache_max_bytes` at 0, for a body past the per-entry bound, or when the client had already left, it still passes unnamed |
| `escrow stopped burning nonces at its budget` | the escrow now queues callers rather than spending on requests it cannot serve |
| `escrow warmed` / `escrow warmup voted on its unfinished nonce` | a new escrow's group was taught the escrow, and the probe's own nonce was settled; the vote line is **Warn** when the vote failed, and `catch_up_error` appears only when the catch-up failed |
| `read trimmed diffs from store` (`user/session.go`) | a host behind the diffs the session holds in memory could not be taught its gap, because the escrow's store read failed; nothing was sent, and the next request to that host retries |
| `heartbeat send failed` (`user/heartbeat.go`) | a heartbeat did not reach the host; `backoff` is how long the host is skipped for, doubling per failure up to eight intervals and cleared by any answer ([escrows.md](./escrows.md), "Height sync") |
| `host blocked for state divergence` (`journal/render_race.go`) | the block does not lift while the process runs and no metric exposes it — "why is this host never picked" is answerable only here |
| `chain snapshot stale` / `chain snapshot recovered` | written on the **edge** only; a failed refresh keeps routing on the previous participants until the last poll that read the epoch and the participants passes `chain_snapshot_max_age_seconds`, after which requests are refused 503. The nonce-ceiling and preserved-set reads fall back within the poll and do not hold that clock back |
| `chain epoch` / `chain blocked requests` / `chain unblocked requests` | written on the **edge** only: `phaseNarrator` (`app/observers.go`) decides the change and the journal writes it; a snapshot that carries no epoch — a first poll that failed — announces none |
| `models are open` (`app/compose.go`) | written at boot and after every config change while any `model_access` entry is `open`; `open_models` names the models any caller reaches without a key, each spending the escrows that serve it |
| `admin request failed` / `admin request refused` (`api/errors.go`) | the operator mutation lines are written on the successful path only, so a failed operator action would otherwise be invisible |

`escrow` is always the escrow id as text. The chain carries the id behind `escrow created`, `escrow recovered from commitment` and `settle tx broadcast` as a number, and `escrow/` and `chain/` convert it to text before the journal writes it, because a JSON collector reads a number as a different type from every other line's `escrow`.

Admin lines carry the action and its subject, **never the request body** — an override payload can hold the admin key. An unkeyed call on an operator route is refused 401 and written down: that is the shape an intrusion attempt takes.

## Metrics

`/metrics`, Prometheus: 96 gateway families beside the Go runtime and process collectors. One of them is the nonce ledger's, the facts it dropped for an escrow it does not hold; everything else it knows is served by its own JSON API (see [accounting.md](./accounting.md)). Grouped by the question they answer:

| Question | Series |
| --- | --- |
| is the gateway serving | `devshard_gateway_requests_total`, `devshard_http_request_duration_seconds`, `devshard_gateway_attempts_terminal_total{visibility="user_visible_winner"}` |
| is it hiding failures | `devshard_gateway_requests_total{outcome="failure"}`, `devshard_gateway_user_requests_with_hidden_failure_total`, `devshard_gateway_attempt_failures_total{visibility="no_winner"}` |
| is it admitting or refusing | `devshard_gateway_limit_rejections_total`, `devshard_gateway_prompt_count_gate_full_total` for a request `DEVSHARD_STATS_TOKENIZER` left on the byte estimate because every prompt count slot was taken, `devshard_gateway_limiter_queue_depth`, and `devshard_gateway_inflight_requests_by_model` against `devshard_gateway_enforced_max_concurrent_requests_by_model`, the cap after overrides and capacity scaling (`devshard_gateway_effective_max_concurrent_requests` is the configured cap before either) |
| how are the hosts | `devshard_gateway_participant_*` (receipt, first content, inter-chunk, transport errors, missed deadlines), `devshard_gateway_host_ejected`, `devshard_gateway_participant_breaker_state`; a host's congestion windows are read from `GET /v1/admin/hosts`, not from `/metrics` |
| is money leaking | `devshard_gateway_ghost_nonces_burned_total`, `devshard_gateway_nonce_holds_total`, `devshard_gateway_timeout_actions_total`, `devshard_gateway_burn_budget_exhausted_total`, and `devshard_gateway_error_miss_verify_rejects_total` for an error the verifiers would not let the gateway claim back ([race.md](./race.md), "Error misses") |
| are the hosts reachable and on time | `devshard_gateway_host_ping_up`, `devshard_gateway_host_ping_rtt_seconds`, `devshard_gateway_host_ping_warm_rtt_seconds`, `devshard_gateway_host_clock_divergence_seconds`, with `devshard_gateway_host_ping_targets`, `devshard_gateway_host_ping_ticks_total` and `devshard_gateway_host_ping_last_probe_timestamp_seconds` saying the probe itself runs ([`hostping/README.md`](../hostping/README.md)) |
| is the shard behind on the votes it owes | `devshard_gateway_owed_timeout_votes`, against `engine_max_concurrent_timeout_votes` ([race.md](./race.md), "The timeout-vote queue") |
| is the chain view healthy | `devshard_gateway_chain_snapshot_healthy`, `devshard_gateway_chain_snapshot_age_seconds`, `devshard_gateway_chain_epoch_phase`, `devshard_gateway_chain_requests_blocked` |
| is the escrow funding planner deciding sensibly | `devshard_gateway_escrow_money{model,class}`, `devshard_gateway_escrow_count{model,state}`, `devshard_gateway_planner_decisions_total{model,action,reason}` `devshard_gateway_funding_guarantee{model}` (below zero the guarantee is short), `devshard_gateway_planner_ignored_marks_total{model,reason}` (depletion marks the planner dropped; a rising `balance_floor` count is routing reporting what the planner already plans for) and `devshard_gateway_escrow_deadline_unsettled_total{model,reason}` (escrows narrated unsettled at or past their settle margin: `settlement_disabled`, `operator_deactivated`, `key_missing`, `deadline_passed`; any `deadline_passed` is money lost); the journal lines `escrow funding planned`, `escrow starved`, `funding guarantee broken` say why. |
| is memory bounded | `devshard_gateway_buffered_response_bytes`, `devshard_gateway_cache_bytes`, `devshard_gateway_capture_bytes_held` |
| are the request records keeping up | `devshard_gateway_accounting_rows_written_total`, `devshard_gateway_accounting_rows_lost_total`, `devshard_gateway_accounting_retention_sweeps_failed_total` |
| is the journal keeping up | `devshard_gateway_journal_money_refused_total`, `devshard_gateway_journal_progress_dropped_total`, `devshard_gateway_journal_late_events_total` |
| is the nonce ledger losing facts | `devshard_gateway_nonce_ledger_unknown_escrow_facts_total`: facts about an escrow the ledger does not hold — one its sweep has not opened yet, which a published escrow's wake keeps to the first sweep's latency, or one its retention pruned |
| where does a request wait inside the gateway | `devshard_gateway_escrow_queue_wait_seconds`, `devshard_gateway_session_step_seconds{step}` (below) |

A race the client left counts as `devshard_gateway_requests_total{reason="client_cancelled"}` unless the escrow itself failed first, which outranks a departure as `reason="escrow_missing"` or `reason="balance_exhausted"`; the faults of its hosts stay on `devshard_gateway_attempt_failures_total{reason}` instead.

A served winner whose nonce is still open when its race ends is counted on `devshard_gateway_attempts_terminal_total` and `devshard_gateway_attempt_failures_total` only when its perf sample is decided ([race.md](./race.md), "A served winner's sample waits for its finish"): as a winner's success when its Finish lands first, and as `failed_not_finished` with reason `not_finished` when its nonce is still open at its vote. Its request is counted on `devshard_gateway_requests_total` at race end; the request's hidden failure is counted at race end when another attempt failed, and otherwise at that decision, once either way.

`devshard_gateway_chain_snapshot_healthy` is the one to alert on first: with a stale snapshot every score, weight and preserved-set decision below it is being made on old data.

### Where the gateway spends a request's time

An escrow hands out nonces one at a time, so its throughput is the inverse of the time one nonce holds its session. Two histograms take that time apart without a profiler, in production and in a load run alike.

`devshard_gateway_escrow_queue_wait_seconds` is how long a request waited in an escrow's dispatch queue before it was handed a nonce (`scheduler/dispatcher.go`, `recordAssigned`). A queue that keeps filling shows here first, before it refuses with `escrow dispatch queue full`.

`devshard_gateway_session_step_seconds{step}` times the session itself (`user/step_timer.go`):

| step | what it times |
| --- | --- |
| `lock_wait_prepare`, `lock_held_prepare` | waiting for and holding the session lock while a nonce is composed |
| `lock_wait_process`, `lock_held_process` | the same while a host's response is applied |
| `store_append_diff` | persisting the nonce's diff, retries included, under the session lock |
| `store_add_signature` | persisting one host signature, off the session lock |
| `host_catch_up` | a send waiting for its host to be caught up |

`lock_held_prepare` bounds the escrow's rate: a mean of 80 ms is at most about twelve nonces a second. When it is high, `store_append_diff` says whether the store is the cause. `step` takes only these values, so the family stays at seven series.

### Cardinality rules

Route labels are **templated** (`/devshard/{id}/…`), never per-escrow, so cardinality does not grow with the escrow set. `/v1/admin/devshards/import` reports under the `/v1/admin/devshards/{id}` label so it lands in the same panel. A recorder keeps every label value it writes non-empty (`metrics/labels.go`, `metricLabel`): an empty label silently merges unrelated series, and a status with no recoverable code reports as `0` (`metrics/vocabulary.go`, `statusNoCode`) rather than as blank. A collector, by contrast, passes its source's values through unchanged, empty ones included.

### Metric changes

A dashboard or alert outside this repository may query a family this gateway does not emit; each row names the one to query instead, or says why none exists. devshardctl emits 95 families, and 19 `devshard_accounting_*` more with its stats on; 53 of the 95, and all 19, are not emitted here. The `participant_window_*` and `escalation_decisions_total` rows are this gateway's own former families.

| Not emitted | Query instead |
| --- | --- |
| `devshard_gateway_no_winner_attempts_total` | `devshard_gateway_attempt_failures_total{visibility="no_winner"}`; an answer that arrived complete and reached nobody is `devshard_gateway_attempts_terminal_total{visibility="no_winner",outcome="success"}` |
| `devshard_gateway_user_visible_wins_total` | `devshard_gateway_attempts_terminal_total{visibility="user_visible_winner"}` |
| `devshard_gateway_critical_user_failures_total` | `devshard_gateway_requests_total{outcome="failure"}` |
| `devshard_inference_timeouts_total` | `devshard_gateway_timeout_actions_total{action=~"completed\|failed"}` |
| `devshard_gateway_escrow_participant_limited` | `devshard_gateway_escrow_blocked_participants > bool 0` |
| `devshard_gateway_participant_window_size` | `GET /v1/admin/hosts`, `input_window_tokens` and `output_window_tokens`. A host's admission window is two windows counted in tokens, per model; a gauge for each would add a per-participant, per-model series to every scrape for a number an operator reads when a host is behaving oddly, so the window state lives on the admin view alone. |
| `devshard_gateway_participant_window_inflight` | `GET /v1/admin/hosts`, `inflight_input_tokens` and `inflight_output_tokens`, for the same reason. `devshard_gateway_participants_exhausted` is the scrape's answer to "how many pairs would refuse an attempt right now". |
| `devshard_gateway_escalation_decisions_total` | `devshard_gateway_attempts_started_total{role="speculative"}` by `reason`; that family carried the race's start plan, never what triggered an escalation |
| `devshard_gateway_heightsync_seconds_since_contact` | `GET /v1/debug/heightsync`, `escrows[].contacts[].since_contact` (milliseconds) |
| `devshard_gateway_heightsync_arming_predicted` | `GET /v1/debug/heightsync`: `contacts[].since_contact` past `idle_timeout` |
| `devshard_gateway_heightsync_host_height` | `GET /v1/debug/heightsync`, `tips[].height` with `originator`, where `fresh` is true |
| `devshard_gateway_heightsync_host_height_lag` | `GET /v1/debug/heightsync`: the highest fresh `tips[].height` minus each fresh slot's |
| `devshard_gateway_heightsync_height_spread` | `GET /v1/debug/heightsync`: highest minus lowest non-zero `tips[].height` |
| `devshard_gateway_heightsync_host_claim_age_seconds` | `GET /v1/debug/heightsync`, `tips[].age` (milliseconds) |
| `devshard_gateway_heightsync_host_sync_state` | `GET /v1/debug/heightsync`, `sync_states[].state` |
| `devshard_gateway_heightsync_cadence_events_total` | `GET /v1/debug/heightsync`, `cadence_counts` |
| `devshard_gateway_heightsync_seconds_since_turnover` | `GET /v1/debug/heightsync`, `seconds_since_turnover` |
| `devshard_gateway_heightsync_turns_abandoned_total` | `GET /v1/debug/heightsync`, `abandoned_turns` |
| `devshard_gateway_heightsync_anchors_last_block` | `GET /v1/debug/heightsync`, `anchors_last_sealed.by_kind` |
| `devshard_gateway_heightsync_anchors_per_block` | `GET /v1/debug/heightsync`, `anchors_per_block` |
| `devshard_gateway_heightsync_turnovers_per_block` | `GET /v1/debug/heightsync`, `turnovers_per_block` |
| `devshard_gateway_heightsync_blocks_without_anchor_total` | `GET /v1/debug/heightsync`, `blocks_without_anchor` |
| `devshard_gateway_heightsync_peer_seen` | `GET /v1/debug/heightsync`, `peer_seen[].bits`; `DEVSHARD_GATEWAY_HEIGHTSYNC_PEER_MATRIX` is not read |
| `devshard_gateway_heightsync_peer_seen_count` | `GET /v1/debug/heightsync`, `peer_seen[].count` |
| `devshard_gateway_heightsync_peer_seen_unseen_total` | `GET /v1/debug/heightsync`: observers whose `peer_seen[].bits` lack the slot |
| `devshard_gateway_heightsync_exchanges_total` | `GET /v1/debug/heightsync`, `overlap` (`total`, `with_section`, `with_stamp`, `agreed`) |
| `devshard_gateway_heightsync_gateway_tip` | `GET /v1/debug/heightsync`, `gateway_tip` |
| `devshard_gateway_rpc_requests_last_minute` | not emitted, non-goal: the gateway does not scrape the hosts' `GET /devshard/stats/rpc`; read it on the host |
| `devshard_gateway_rpc_banned_last_minute` | not emitted, non-goal: same, a host's own rate-limit counts |
| `devshard_gateway_rpc_endpoint_requests_last_minute` | not emitted, non-goal: same |
| `devshard_gateway_rpc_endpoint_banned_last_minute` | not emitted, non-goal: same |
| `devshard_gateway_rpc_zone_requests_last_minute` | not emitted, non-goal: same |
| `devshard_gateway_rpc_zone_banned_last_minute` | not emitted, non-goal: same |
| `devshard_gateway_rpc_attach_attempts_last_minute` | not emitted, non-goal: same |
| `devshard_gateway_rpc_attach_banned_last_minute` | not emitted, non-goal: same |
| `devshard_gateway_rpc_reconnects_last_minute` | not emitted, non-goal: same |
| `devshard_gateway_rpc_stats_up` | not emitted, non-goal: there is no scrape whose health to report |
| `devshard_gateway_rpc_minute_unix` | not emitted, non-goal: same |
| `devshard_gateway_rpc_stats_partial` | not emitted, non-goal: same |
| `devshard_gateway_capacity_scale` | `devshard_gateway_capacity_scale_by_model`: caps are scaled per model, so there is no gateway-wide scale |
| `devshard_gateway_capacity_total_weight` | `devshard_gateway_capacity_total_weight_by_model`, for the same reason |
| `devshard_gateway_capacity_baseline_weight` | `devshard_gateway_capacity_baseline_weight_by_model`, for the same reason |
| `devshard_runtime_reserved_tokens` | not emitted, non-goal: the escrow pick no longer scores by reserved prompt tokens; input tokens in flight are `devshard_gateway_inflight_input_tokens_by_model`, an escrow's load `devshard_runtime_active_requests` |
| `devshard_gateway_picker_choice_total` | not emitted, non-goal: no per-escrow pick counter; how load spreads across escrows is `devshard_runtime_active_requests{devshard_id,model}` |
| `devshard_gateway_participant_limit_rejections_total` | a send a host's window or cut-off refuses is `devshard_gateway_ghost_nonces_burned_total{reason=~"participant_window_full_no_send\|participant_cut_off_no_send"}` by `participant`; a request no host could take is `devshard_gateway_requests_total{outcome="failure",reason="no_attempts"}` |
| `devshard_gateway_participant_quarantine_state` | `devshard_gateway_participant_breaker_state{state!="closed"} == 1` and `devshard_gateway_host_ejected == 1`: a per-host window with a cut-off replaced quarantine |
| `devshard_gateway_participant_quarantine_transitions_total` | `changes(devshard_gateway_participant_breaker_state{state="open"}[...])`, with the journal lines `host cut off after transport faults` and `host back after its cut-off` |
| `devshard_speculative_decisions_total` | `devshard_gateway_attempts_started_total{role="primary"}` by `reason`: `primary` started alone, `primary_suspicious` and `primary_degraded` were hedged from the start |
| `devshard_speculative_attempt_starts_total` | `devshard_gateway_attempts_started_total{role="speculative"}` by `reason` |
| `devshard_gateway_slot_decisions_total` | `decision="real_send"` is `devshard_gateway_attempts_started_total`; `decision="ghost_no_send"` is `devshard_gateway_ghost_nonces_burned_total`; a warmup probe is the journal line `escrow warmed` |
| `devshard_gateway_error_miss_rejects_total` | not emitted, non-goal: devshardctl never incremented it; a rejected error miss is `devshard_gateway_error_miss_verify_rejects_total{cause,completeness}` |
| `devshard_gateway_chat_cache_total` | `result="hit"` is `devshard_gateway_cache_hits_total` (no `model`); `stored` and each `skipped_<reason>` are `devshard_gateway_cache_results_total{model,result}`, the refusal named without the `skipped_` prefix |
| `devshard_gateway_startup_skipped_escrow` | not emitted, non-goal: an escrow whose session will not open is retried every lifecycle tick rather than disabled for the process's life; the journal line `devshard could not be opened, retrying` names it |
| `devshard_gateway_host_ping_ticks_skipped_total` | `devshard_gateway_host_ping_ticks_total{outcome="skipped"}` |
| `devshard_gateway_host_ping_participant_info` | not emitted, non-goal: every ping series is already labelled `participant_key` |
| `devshard_gateway_host_ping_probe_kind` | not emitted, non-goal: the `source` label on `devshard_gateway_host_clock_divergence_seconds` says how a host's time was read |
| `devshard_accounting_assigned_nonces` | `GET /api/v1/epochs/current/participants` on `DEVSHARD_STATS_PORT`, `assigned_nonces` |
| `devshard_accounting_disposition` | the same route, `dispositions` and `counters[]` |
| `devshard_accounting_delivery` | the same route, `counters[]` by `terminal` and `usage` |
| `devshard_accounting_timeout_outcome` | the same route, `timeout_outcomes` and the timeout fields of `counters[]` |
| `devshard_accounting_protocol_misses` | the same route, `protocol_misses` |
| `devshard_accounting_protocol_invalid` | the same route, `protocol_invalid` |
| `devshard_accounting_unresolved_challenges` | the same route, `unresolved_challenges` |
| `devshard_accounting_in_flight` | the same route, `in_flight` |
| `devshard_accounting_in_flight_requests` | the same route, `in_flight_requests`; the scrape's nearest is `devshard_gateway_host_inflight_requests{participant_key}` |
| `devshard_accounting_timeout_pending` | the same route, `timeout_pending` |
| `devshard_accounting_pending_classification` | the same route, `pending_classification` |
| `devshard_accounting_unclassified` | the same route, `unclassified` |
| `devshard_accounting_overclassified` | the same route, `overclassified` |
| `devshard_accounting_unknown_reason_total` | the same route, `unknown_reason_total` |
| `devshard_accounting_cross_check_error` | the same route, `cross_checks` |
| `devshard_accounting_finding` | the same route, `findings` |
| `devshard_accounting_disposition_drops` | `devshard_gateway_journal_money_refused_total`, facts the journal refused past its ceiling before they reached the ledger, and `devshard_gateway_nonce_ledger_unknown_escrow_facts_total`, facts the ledger dropped for an escrow it does not hold |
| `devshard_accounting_writer_errors` | not emitted, non-goal: the ledger serves no Prometheus; a failed save is the log line `nonce accounting save failed` |
| `devshard_accounting_recording_errors` | not emitted, non-goal: the same; a refused fact is the log line `nonce accounting refused a fact`, except one about an escrow the ledger does not hold, which `devshard_gateway_nonce_ledger_unknown_escrow_facts_total` counts instead |

The gateway emits neither label nor the value below, so a selector that names one matches nothing:

- `devshard_gateway_participant_transport_errors_total` has no `path_kind`: every error it counts is an inference request.
- `devshard_gateway_user_requests_with_hidden_failure_total` has no `severity`: every hidden failure it counts is on a protected request.
- `devshard_gateway_host_ping_up`, `devshard_gateway_host_ping_rtt_seconds`, `devshard_gateway_host_ping_last_probe_timestamp_seconds` and `devshard_gateway_host_clock_divergence_seconds` have no `host`: the host is `participant_key`, the participant's key rather than the dial address.
- `devshard_gateway_attempts_started_total` has no `quarantine_mode`: the per-host window and cut-off replaced quarantine.
- `devshard_gateway_host_ping_ticks_total` counts skipped waves too: what the devshardctl family counted is `{outcome="started"}`.
- `outcome="due"` on `devshard_gateway_timeout_sweep_total`: `applied` plus `failed` is what a tick found, short of it only on a tick that shutdown cut off mid-round.

Participant-labelled race series — `devshard_gateway_attempts_*`, `devshard_gateway_attempt_failures_total`, `devshard_gateway_timeout_actions_total`, `devshard_gateway_stream_carry_overflow_total` and every `devshard_gateway_participant_*` family except the breaker gauge — are deleted once their participant and model go unwritten for `perf_host_staleness_seconds`. A host that returns afterwards starts from fresh counters, which `rate()` reads as a reset.

`devshard_gateway_participant_breaker_state` stops reporting a pair the participant limiter forgot on the same window, and `devshard_gateway_participants_tracked` counts only the pairs it still holds.

## Tracing

`DEVSHARD_OTEL_ENABLED` turns OpenTelemetry tracing on, exporting to `OTEL_ENDPOINT` with `OTEL_HEADERS`; spans carry the service name `devshardctl` (`observability.GatewayServiceName`), which the Tempo queries and dashboards select on. Off, the tracer is a no-op. A broken exporter or a malformed header degrades to no tracing and a warning, never to a gateway that will not start (`app/tracing.go`), and spans are flushed for up to ten seconds at shutdown.

Every chat request opens a `gateway.request` root span (`api/routes.go`, `chat`), and the race runs under it. The transport stamps `traceparent` and `X-Request-Id` on every host call made under a live span, over JSON and Connect alike, so a host's spans join the request's trace; `trace_id` is the join key with the logs. Timeout votes and settlement run after the request on their own context and start no trace. Ids belong in span attributes and log lines, never in metric labels ([`devshard/docs/gateway-tracing.md`](../../../docs/gateway-tracing.md)).

How each escrow reaches its hosts is in two series: `devshard_gateway_escrow_sessions_total{path}` counts each escrow-host binding once, by `h2` (Connect over HTTP/2) or `json`, and `devshard_gateway_host_rpc{peer,mode}` is 1 per host and mode while a live escrow binds it (`registry/registry.go`, `bindPeerRPC`; `metrics/peer_rpc_recorder.go`).

## Reading the gateway's state

| To see | Call |
| --- | --- |
| what is routable, and the config in force | `GET /v1/admin/state` |
| the stored admin overrides, as a `PUT` would write them back | `GET /v1/admin/settings` |
| every escrow row the process owns | `GET /v1/admin/devshards` |
| the full participant keys to paste into either routing list | `GET /v1/admin/hosts` — never a log line's short host label, which is the last eight characters and matches nobody |
| rotation's last outcome per model | `GET /v1/debug/rotation` |
| which slot answered which height-sync turn | `GET /v1/debug/heightsync` |
| one escrow's protocol state | `GET /devshard/{id}/v1/state` |
| what happened to one request's nonces | `GET /v1/requests/{id}` |
| hosts the gateway distrusts | `GET /v1/admin/suspicious-hosts` |
| which filters each model runs, and where they come from | `GET /v1/admin/filters` |
| why one host is or is not taking work | `GET /v1/admin/hosts` |

`GET /v1/debug/heightsync` reads each session's published height-sync view, not the session itself, so the read never waits on an escrow's lock (`registry/session.go`, `HeightSyncView`). A nonce commit and an applied response only mark that view stale and leave its snapshot, which takes the session lock again, to the view's trailing flush (`user.Session.publishHeightSyncViewOffPath`): the view lags the session by about its publish interval.

An escrow row in `GET /v1/admin/state` and `GET /v1/admin/devshards` carries, besides the gateway's own labels (`rotation_role`, `rotation_epoch`, `active`, `settlement_pending`), the chain facts the gateway resolved for it: `chain_epoch`, the epoch the chain stamped on the escrow, whose settle deadline the deadline rule reads (`0` while unresolved); `amount`, the escrow's funded amount (`0` while unresolved); and `gone_from_chain`, true once the chain no longer holds the escrow, which takes it out of service and out of the unsettled count. A `GET /v1/admin/devshards` row whose session is resident (published or still draining) also carries `runtime` with the session's current `nonce`; a row with no resident session has none, which is how a reader tells a live escrow from a stored one (`api/admin_devshards.go`).

### What a host answer carries

`GET /v1/admin/hosts` joins the two snapshots that already exist, one row per participant and model. Nothing is computed for it: each field is a value one of the two limiters or the tracker already holds, asked for on demand rather than waited for at the next scrape. For the congestion windows it is the only surface there is — no gauge carries them.

| Field | Where it comes from |
| --- | --- |
| `ejected`, `degraded`, `inflight`, `decode_seconds_per_token` | the performance tracker: whether routing withholds this host, whether it would without the pool-wide cap, and how fast it decodes |
| `input_window_tokens`, `output_window_tokens`, `inflight_input_tokens`, `inflight_output_tokens`, `cutoff`, `backoff_count`, `available` | the participant limiter: the size of each congestion window, what is in flight against it, the cut-off's state, how deep its backoff has gone, and whether the host would be admitted right now |
| `context_limit`, `version_refusals`, `tool_refusals`, `context_refusals` | what this host's build refused |
| `suspicious` | whether an operator pinned it |

`backoff_count` is the answer to "was this host cut off once or eleven times": it is what makes the next cut-off a minute long instead of five seconds. `degraded` beside a false `ejected` means the pool-wide cap is withholding the withholding, because too many of that model's hosts are failing at once.

## When something is wrong

| Symptom | Look at |
| --- | --- |
| every request 429 | `limiter_queue_depth` and `effective_max_concurrent_requests` — the weight model may have scaled capacity to almost nothing after an epoch switch |
| every request 503 with no model listed | the chain snapshot is stale, or the escrow set is empty — check `chain_snapshot_healthy` and `/v1/admin/devshards` |
| 503 on a healthy-looking gateway | `buffered_response_bytes` at the ceiling: non-streaming replies are holding the whole budget |
| a model returns 401 to every caller but the admin | no `model_access` entry names it — set it through the `model_access` override |
| the admin surface 404s | `DEVSHARD_ADMIN_API_KEY` is unset |
| burns climbing | `ghost_nonces_burned_total` by reason — see [accounting.md](./accounting.md) |
| prompts past a model's context reach hosts | the log line `prompt tokenizer could not be loaded`, `prompt tokenizer pin could not be read`, `model has no tokenizer the gateway reads` or `prompt tokenizer parse panicked` for the model, or no context length for it — see "The context check" |
| shutdown reports "abandoned with work still running" | a host stopped answering and the drain hit the grace period; the votes it owed were not paid |

To reproduce an escrow lifecycle problem (create, drain, timeout votes, settle, prune) without a stand, use the scenario tests in [`scenarios/`](../scenarios/README.md); the `loadsim` test described in [CONTRIBUTING.md](../CONTRIBUTING.md#load-simulation) stays the profiling tool.

## The funding planner

The planner alone creates and retires escrows. An escrow whose balance falls under the model's floor keeps serving the requests it can afford: routing spends starved residue first on small requests, and a depletion mark other than the nonce cap is dropped and counted instead of retiring the escrow. At least `full_context_slots` full escrows are kept (the guarantee), capacity follows demand, and creates are bounded by `max_unsettled` and by a rate bucket of two at once, then one per refill. The epoch bridge keeps its window; the planner plans nothing inside it, and after PoC it funds the regular set and retires the old temps once the spread is met. Read `escrow funding planned` and `devshard_gateway_planner_decisions_total` to see what it decided.

What to watch:

| Signal | Healthy |
| --- | --- |
| `devshard_gateway_funding_guarantee{model}` | at or above 0; below 0 the guarantee is short, and `funding guarantee broken` says why (wallet, breaker, PoC, bridge window) |
| `devshard_gateway_escrow_money{model,class}` | `free` follows demand; `stuck` stays near 0 |
| `devshard_gateway_planner_decisions_total{model,action,reason}` | creates and retires track demand; a steady stream of `capacity` creates with a flat `max_unsettled` is churn against the budget |
| `devshard_gateway_planner_ignored_marks_total{model,reason}` | balance marks dropped by design; a rising `balance_floor` count is routing reporting what the planner already plans for |
| `devshard_gateway_escrow_deadline_unsettled_total{model,reason}` | 0; any `deadline_passed` is money lost |

## Stop token ids

A request's `stop_token_ids` are checked against the vocabulary the chain pins for the routed model in the current epoch, the same size the executor and the validator bound token ids by (`app/stop_token_vocabulary.go`). The gateway reads the model's Hugging Face repo and commit from the epoch's model snapshot on chain and the `vocab_size` from that commit's `config.json` on the Hugging Face hub, so a gateway that accepts `stop_token_ids` needs egress to `huggingface.co`. A resolved size is cached for good, since a pinned commit never changes. The first request for a model in an epoch waits for the lookup, up to 10 seconds; a failed lookup is retried after 10 minutes, and until then every request with `stop_token_ids` for that model is refused. The gateway looks the size up only for a model one of its escrows serves (`api/routes.go`, `servedModelVocabulary`), so a client naming arbitrary models cannot grow the cache or reach the chain; for any other model the field is refused like an unknown size. Until the first epoch is known, or while the size cannot be resolved, a request carrying `stop_token_ids` is refused with a 400 rather than forwarded with an id nobody proved safe. A request without them never triggers the lookup. See [`filters/README.md`](../filters/README.md), "Stop token ids".

## The context check

A chat request whose prompt and `max_tokens` its model's context cannot hold is refused with a 400, `invalid_request_error` / `context_length_exceeded`, before any nonce exists; the route's status series counts it, and it is not captured. The prompt is counted with the `tokenizer.json` of the Hugging Face repo and commit the epoch's model snapshot pins (`app/prompt_tokens.go`), so a gateway with a chain needs egress to `huggingface.co` for the check, as for `stop_token_ids`. Each served model's file, up to 20 MB today, is fetched once per pinned commit, in the background, on the first request for the model; until it loads, and for a model whose commit ships no `tokenizer.json` the gateway reads (Kimi-K2.6 ships a tiktoken model), requests go to a host unchecked. A failed fetch is retried after 10 minutes; when the pin moved and the new file fails to load, the model is unchecked until it loads, rather than counted with the previous file. The log reads `prompt tokenizer loaded` on a load, `model has no tokenizer the gateway reads` for a pin that is not checked, `prompt tokenizer parse panicked` (an error, also not checked until the pin moves) for a file whose parse panicked, `prompt tokenizer could not be loaded` on a failed fetch, and `prompt tokenizer pin could not be read` when the chain query fails, in which case a tokenizer already loaded keeps counting. The context length is the `--max-model-len` governance registers, else `model_limits[m].max_model_len`; a model with neither is not checked. See [request.md](./request.md), "Step 6 in detail".

## Where to change what

| To change | Go to |
| --- | --- |
| a default | `config/defaults.go`, `Defaults()` |
| which variables are read | `env/env.go` — and nowhere else |
| what an admin may override at runtime | `config.Overrides` |
| a route or its auth tier | `api/routes.go`, `routes()` |
| what shuts down when | `app/lifecycle.go`, `shutdownOrder` |
| a metric or its labels | `metrics/` |

## One process per escrow set

The gateway is one process per set of escrows. It keeps its state in its own SQLite store under `DEVSHARD_STORAGE_DIR` and shares it with no other process: two gateways given the same escrows, through `DEVSHARDS_JSON` or the admin API, would each sequence their nonces and each ledger would see only its own share, which the chain rejects as diverged state. The devshardctl gateway's Postgres settings (`PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PG_CONNECT_TIMEOUT`) are not read; when any is set the gateway logs `legacy Postgres settings are ignored` at boot, naming them (`env.LegacyPostgresVariablesSet`).

## Moving from devshardctl

The gateway reads none of devshardctl's state: its own store starts empty ([`rules.md`](./rules.md), "Migrated state"), so whatever devshardctl's store alone remembers is lost at the switch. That is its open create commitments, its parked and settle-pending escrows, and every escrow it served without a session directory the new gateway can import. An escrow nobody settles is pruned by the chain two epochs after the one that funded it and its amount split across its group, so the operator loses the refund. Cut over in this order:

1. Stop new work on devshardctl (its kill switch), and let its in-flight requests finish.
2. Settle every escrow it holds, or let it drain and settle them, and wait until it reports no open create commitment and no settle-pending escrow.
3. Copy the session directory of every escrow still worth serving and import each through `POST /v1/admin/devshards/import`, naming the variable that holds its key; an escrow with nothing left to serve is settled in step 2 instead.
4. Set `model_access` through the settings override before opening the gateway to clients: every model starts admin-only ("Who may call what").

An escrow the chain already reports settled is never published: a seeded one has its session refused at open and its row deleted ("Boot", step 5), and importing one fails with `bridge.ErrEscrowSettled`, its row left for the next publication to delete. An escrow that settles on chain while it serves is parked by the escrow check that finds it settled, and the settle path then drops it without a second broadcast.

## The two routing lists

`participant_allowlist` narrows **which participants this gateway routes to**. `unthrottled_participants` names participants whose congestion window and ejection it declines to apply, and admits them whether or not the allowlist names them. Both are set through `/v1/admin/settings`, by `PUT` or `POST`, and nowhere else: there is no env value and no default behind them.

**That makes them one `PUT` away from being cleared.** A `PUT` replaces the stored overrides document whole, so a `PUT` that does not resend a list drops it, and a dropped narrowing fails open: the gateway routes to everybody again without refusing anything. The same is true of a fresh or lost store. A `POST` leaves a list it does not name in place. Read the lists back either way: the `settings written` audit line carries `participant_allowlist` and `unthrottled_participants` as they stand after the call.

An entry must look like an address. The shape is checked when the settings document is built, so a short host label pasted from a log or a Grafana `host=` tag is refused at the settings call rather than narrowing dispatch to nobody.

Neither list restrains anything but routing. Host pings, catch-up, finalize, the retirement flush, the timeout sweep and the warmup prober all address the group the chain gave the escrow. See routing.md, "Paths that ignore the routing lists, by design".

## Settling an escrow that is still busy

`POST /v1/admin/devshards/{id}/settle` answers 409 `devshard busy` while requests are still spending the escrow's nonces, and for up to a minute after its timeout sweep failed a vote, so the vote is retried before finalize pays that record in full (escrows.md, "Settlement and retirement"). By then the escrow has already been parked and taken out of routing, so it is draining: calling again once it has drained settles it.

`POST /v1/admin/devshards/{id}/settle?force=true` does not wait. This route reads `force` from the query alone and takes no body; only the batch route below also reads it from its body. It crosses the busy check and nothing else — a second admin settle of the same escrow waits for the first to return and answers 404 `unknown devshard` if the first settled it, one the rotation lifecycle has in flight is still refused, and an escrow already settled is still not rebroadcast. **The requests still in flight are failed for their callers, and their full reserved cost is paid to the hosts that held them**, because finalize closes the door on new nonces and the records still live take the settlement default. The forced call is written to the audit log as `escrow settled under force`, with `in_flight` naming how many requests it overrode.

A forced settlement runs on an escrow that still has requests, so the one that finishes last would close the escrow's store while finalize is still collecting signatures. Finalizing, building the settlement, inspecting and the admin finalize and signature-collection routes therefore each take a settlement hold, and the draining escrow closes only after they return. The hold is counted apart from requests, so it neither makes the escrow busy nor moves routing (`registry/views.go`, `Registry.HoldSettlement`).

A settle that has started runs to its end even if the client leaves, because a settle cut short between its timeout sweep and the end of finalize would pay full reserve on the records the sweep did not reach: it runs on a context detached from the request, bounded by `adminSettleTimeout` (5 minutes; `api/admin_settle.go`). That timeout starts counting before the settle waits for the escrow's operator lock, and it does not interrupt that wait: a settle queued behind another write of the same escrow waits until that write returns, then has only what is left of the 5 minutes. A settle that does time out stops before finalize or partway through it, and the next settle resumes the finalize where it stopped.

## Settling an escrow the chain has pruned

The chain deletes an escrow a couple of epochs after the one that funded it. A settle of one it no longer holds answers 410 `escrow pruned on chain` and drops the escrow's row, so it is gone from the list afterwards; nothing is finalized or broadcast. See escrows.md, "Settlement and retirement".

## Creating an escrow the wallet cannot pay for

`POST /v1/admin/escrows` reads the wallet's spendable balance before it builds the transaction, and answers 402 naming the wallet, what it holds and what the amount plus the fee needs, without broadcasting anything.

## Settling a list of escrows

`POST /v1/admin/devshards/settle` with the body `{"escrow_ids": ["93033", "93040"], "batch_size": 8, "force": true}` settles each escrow the way the single-escrow route does. `force` crosses the busy check for every escrow in the list; `?force=true`, as on the single-escrow route, does the same. A repeated id is settled once.

The answer is a stream of NDJSON (`application/x-ndjson`): `200` goes out before the first settle starts, then one line per escrow the moment it finishes, in finishing order (its transaction, or the status and message the single route would have answered with), and a last line `{"settled": N, "failed": M}`. A line for a client that has left is dropped, and once it has left no further escrow of the list is started; one already settling finishes. A list the route refuses outright (no ids, too many, a bad `batch_size`) still answers a plain `400`. Read it with `curl -N`:

```bash
curl -sS -N -X POST "$GATEWAY/v1/admin/devshards/settle" \
  -H "Authorization: Bearer $ADMIN_KEY" -H "Content-Type: application/json" \
  -d '{"escrow_ids": ["93033", "93040"], "batch_size": 8, "force": true}' | jq -c
```

- **`batch_size` at a time, four by default, sixteen at most.** Building a settlement asks every host in the escrow's group for its signature, so an unbounded batch is an unbounded fan-out onto the same hosts (`api/admin_batch.go`, `defaultBatchSize`, `maxBatchSize`).
- **At most fifty per call.** Each settle runs to its end on its own context, but a caller that leaves stops the list from going on, so a list long enough to outlive the operator's patience would stop halfway, with transactions already broadcast (`batchEscrowLimit`).

## Deactivating a list of escrows

`POST /v1/admin/devshards/deactivate` with the body `{"escrow_ids": ["93033", "93040"], "batch_size": 8}` deactivates each escrow the way `/v1/admin/devshards/{id}/deactivate` does: routing stops first, then the row goes inactive. A repeated id is deactivated once, and an id the gateway has no row for answers `404` on its own line.

The answer has the shape of the settle batch: `200` NDJSON with one line per escrow in finishing order (`{"escrow_id": "93033"}`, or its `status` and `error`), then `{"deactivated": N, "failed": M}`. The same limits apply — `batch_size` four by default and sixteen at most, fifty ids per call — and a list it refuses outright answers a plain `400` (`api/admin_batch.go`, `admitBatch`).

```bash
curl -sS -N -X POST "$GATEWAY/v1/admin/devshards/deactivate" \
  -H "Authorization: Bearer $ADMIN_KEY" -H "Content-Type: application/json" \
  -d '{"escrow_ids": ["93033", "93040"]}' | jq -c
```

A registration (`POST /v1/admin/devshards`) with `activate` false deactivates an escrow already routed the same way, so its storage can be imported once its session is released.

A deactivation does not wait on the nonce ledger: the escrow's last reading is taken in memory and the ledger's own goroutine writes it out (`accounting.Service.RequestFlush`). What it can still wait on is the escrow's last diff to its hosts, up to five seconds when the escrow holds gossiped transactions no request composed (`registry/retirement_flush.go`).
