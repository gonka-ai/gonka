# Operating the gateway

The gateway is one binary with one HTTP port and one storage directory; escrows, hosts and epochs are read from the chain and rebuilt at boot. This document describes what an operator sees of the process: the routes it serves and who may call them, the kill switch, what it does at boot and shutdown, and what it keeps on disk.

| For | Read |
| --- | --- |
| every environment variable, admin override and model filter setting | [configuration.md](./configuration.md) |
| logs, metrics, traces and the state routes | [observability.md](./observability.md) |
| procedures: release, rollback, backup, settle, deactivate, troubleshooting | [runbook.md](./runbook.md) |
| the compose stack | [`deploy/README.md`](../deploy/README.md) |

## What is exposed

Three tiers, and the tier decides both who may call it and whether the kill switch applies:

| Route | Auth | Survives the kill switch |
| --- | --- | --- |
| `/v1/chat/completions` | per-model (below) | no |
| `/v1/models` | none — it lists what is routable, nothing caller-specific | no |
| `/v1/status` | none, as above | yes — a gateway disabled on purpose is not unhealthy |
| `/devshard/{id}/v1/chat/completions`, `.../models`, `.../status` | as above, pinned to one escrow | no |
| `/metrics`, `/healthz` | none | yes — the gateway's compose healthcheck reads `/healthz`, which answers 200 without checking anything else ([`deploy/README.md`](../deploy/README.md)); at most 4 scrapes run at once and each is cut at 10 s, both answered 503 ([`metrics/README.md`](../metrics/README.md), "Scrapes") |
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

### Who may call what

`api/routes.go`, `authorizeModel`. Three tiers per model, from `access` under `models.<id>` (or the network `access` for a model with no block of its own):

| Tier | Who |
| --- | --- |
| `open` | anyone |
| `api_key` | a caller presenting an active client key ("Client API keys" below) |
| `admin_only` | a caller presenting `DEVSHARD_ADMIN_API_KEY` |

**Two behaviours to know before editing it:**

- A model with no `access` of its own and no network `access` is `admin_only`. A deployment starts with no entries, so until the operator names a model `open` or `api_key` only the admin reaches it, and creating a client key alone opens nothing. While any model is `open` the gateway logs a warning naming it, at startup and after every admin override, since an open model spends the operator's escrows for any caller.
- With `DEVSHARD_ADMIN_API_KEY` unset the whole admin surface answers **404, not 401** — the routes do not exist rather than rejecting the caller. A "route not found" on `/v1/admin/…` usually means the key is missing, not the path. Without it no one can set `access` or create a client key either, so every model stays admin-only and the gateway serves no client.

An admin key satisfies every tier, so admin calls never need a second key. A `PUT` of the settings replaces the whole override set, so a `PUT` that leaves out `models` drops every model's admin block, and a model with no `access` of its own is then reachable only as the network `access` and `GATEWAY_MODELS_JSON` allow; a `POST` changes only the fields it names ([configuration.md](./configuration.md), "The three layers").

The `models` setting is also the list of models the gateway offers (`config.Config.Offers`): a model named in it answers 503 when nothing currently routes to it, and a model named in it nowhere answers 400 while any model is routable — an empty registry answers 503 for every model instead.

### Client API keys

Client keys live in the `api_keys` table of `gateway.db`, **in plain text**: the storage directory and every archive of it are as secret as the keys. They are managed through the admin API and apply on the next request, with no restart.

| Call | What it does |
| --- | --- |
| `GET /v1/admin/api-keys` | lists every key as `id`, `source`, `label`, `created_at`, `revoked` and `revoked_at`, never a key value |
| `POST /v1/admin/api-keys` | creates a key from `{"label": "...", "key": "..."}`, both optional; without `key` the gateway generates one; the answer carries the key |
| `GET /v1/admin/api-keys/{id}` | one key with its value, audit-logged without it; a legacy environment key has no value |
| `DELETE /v1/admin/api-keys/{id}` | revokes the key; the row stays, and a second revoke answers 200 with the first revoked time |

```
curl -X POST -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" -d '{"label":"billing"}' "$GATEWAY/v1/admin/api-keys"
{"id":"3f9a1c2e","source":"database","label":"billing","created_at":"2026-01-02T03:04:05.000000000Z","revoked":false,"key":"sk-..."}
```

The `id` is the first eight hex characters of the key's SHA-256, the `api_key_id` the request record carries ([observability.md](./observability.md), "The request record"). A create whose `id` exists, revoked or not, answers 409, so an id is never reused. A supplied key shorter than 16 characters, containing whitespace (no `Bearer` header can carry it) or equal to the admin key answers 400. To rotate a key, create the new one and revoke the old one.

An `api_key` model needs an active key. Revoking the last active key while the network `access` or any model's `access` is `api_key` answers 409, and a settings write that leaves such a model with no active key answers 400. A gateway whose database holds no key and whose settings put a model on `api_key` still starts, and logs a warning that an `api_key` model has no active key, so the first key can be created. A settings write is refused only when it creates that state, so the kill switch and every other setting stay writable until a key exists, and the rule binds again once a key does. A stored block for a model `rotation_models_json` no longer lists is ignored, so it never counts as an `api_key` model. A `DEVSHARD_ADMIN_API_KEY` equal to an active database client key fails the boot: a database client key must differ from the admin key. The `api key created`, `api key read` and `api key revoked` audit lines carry the `api_key_id` and `api_key_label`.

### Legacy inputs

- Legacy `model_limits` (deprecated, removal planned for v7 or v8): the settings key is still accepted and read as `models.<id>` with the same field names; set `models.<id>` instead, which wins field by field, and a boot or a write holding the key logs a warning.
- Legacy `model_access` (deprecated, removal planned for v7 or v8): the settings key is still read as `models.<id>.access`; set `models.<id>.access` instead, which wins when both name the model, and a boot or a write holding the key logs a warning.
- Legacy `DEVSHARD_API_KEYS` (deprecated, removal planned for v7 or v8): the variable is still read, comma-separated and each key at least 16 characters, and its keys are active beside the database keys; create them through `POST /v1/admin/api-keys` instead. The list and a read by id show them with `source` `env`, no value and no `created_at`, and they cannot be revoked or created again through the API (409): to remove one, move its clients to a new key, then unset the variable.

### The kill switch

`modes.disabled` (env `DEVSHARD_GATEWAY_DISABLED`, or `disabled` through the admin settings endpoint) stops serving clients while leaving `/metrics`, `/healthz`, `/v1/status`, the admin surface and the recovery surface up — so a gateway can be taken out of service and still be inspected, settled and drained.

With `disabled_redirect_url` set it answers **308** with the new URL in both the header and the body; without one, **503** with `disabled_message`. The distinction matters to clients: a 308 is a permanent move, a 503 is "come back later".

## Boot

`app/lifecycle.go`, `bootOrder` and `startAll`. The order is a declared list rather than a sequence of calls, so a test can read it back (`app/boot_order_test.go`), the way the shutdown order is. It is load-bearing:

1. the chain observer starts — nothing downstream can score a host before a snapshot exists;
2. the warmup prober starts;
3. the host pings start — observability alone, and they read the live set per wave, so starting before anything is published costs nothing ([`hostping/README.md`](../hostping/README.md));
4. `seedDevshards` applies `escrows_json` to the store — a seeded escrow that names no key variable is **refused**, not silently accepted. It leaves a row it already holds alone, but settlement deletes the row of an escrow it settled, so on every later boot the seed writes that escrow back as active;
5. `publishEscrows` opens a session per active escrow and publishes it for routing, bounded by `MaxConcurrentRuntimeBuilds` (16) so a large set does not open 200 sessions at once. An escrow the chain no longer has is marked inactive; one the chain reports settled is refused before its session opens (`bridge.ErrEscrowSettled`) and its row is deleted, as settlement deletes it, so a seed written back by step 4 is never published or routed; one whose key is missing, or whose session would not open for any other reason, stays active and unpublished, is logged as `devshard could not be opened, retrying`, and is retried every lifecycle tick (15 s) until it opens, so one escrow never keeps the others from serving. A group member the chain no longer lists does not stop a session opening: its slot fails every call as a down host's would (`user.ErrHostAbsentFromChain`) until the session is reopened, which step 9 does once the chain lists the participant again;
6. the nonce ledger starts, so nothing above it files a fact into a sink that is not yet reading;
7. the refusal votes a previous process left owed in the store are owed again, each holding its escrow until it posts, so the lifecycle below does not settle an escrow ahead of the vote; a vote whose escrow did not open in step 5 is dropped ([race.md](./race.md), "Owed refusal votes across a restart");
8. the escrow lifecycle manager starts its 15 s tick;
9. the store's write notifications start republishing escrows on change, and on a timer while step 5 left an escrow unopened; the same loop probes the chain for participants absent when an escrow's session opened, and reopens that escrow once one is listed again, all of a model's such escrows in the same probe while another escrow of the model with no dead slot serves it ([routing.md](./routing.md), "A slot the chain did not list at open");
10. the HTTP listener opens — **last**, so the first request meets a gateway that is fully assembled.

`startAll` stops at the first step that fails and names it, and `serve` shuts down cleanly rather than serving half-built. Steps 4, 5 and 7 are the only ones that can fail: the rest start a goroutine or a listener and return. Step 7 fails only when the store cannot be read. Step 5 fails only when the store cannot be read or a row cannot be retired or marked inactive, not when an escrow's session will not open.

## Shutdown

`app/lifecycle.go`, `serve` and `shutdownOrder`. Before the first step, `serve` cancels the background context the chain observer, warmup prober, host pings, nonce ledger, escrow lifecycle and devshard-write republish were started under (`stopThenShutDown`), after a boot that failed as after a signal. Then thirteen steps, in this order, bounded by the grace period, with up to one more second from the journal step's floor:

| # | Step | Why here |
| --- | --- | --- |
| 1 | http server | stop taking new work first; open requests get the grace period less the reserve the steps below keep (`laterStepsReserve`, 5 s) and less the cancelled wait to finish uncancelled (`httpGracefulShare`, 4 s of the 10 s), so a short answer or an operator call still running completes; only if the server is not idle by then is every request still open cancelled, and the step waits at most 1 s more for those handlers (`httpCancelledWait`), so a stream that would never end holds the step for 5 s at most and the steps below keep their reserve; a race whose client is cancelled this way drains in step 2 like any client departure |
| 2 | races | in-flight requests finish, and the losers post the votes they owe that are already due when the engine stops, within the grace period (10 s); a vote not yet due is not waited for, and a refused nonce's vote falls due about 65 s after its refusal, so one refused in the last minute before a restart is left in the store and posted by the next boot (step 7 of "Boot"), unless its prompt was over 1 MiB or the store's queue was full, in which case finalize pays it in full |
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

On the first chain snapshot and whenever the effective epoch moves forward, one background pass removes every `escrow-<id>` directory whose `epoch_<N>.db` is more than `DEVSHARD_SESSION_RETENTION_EPOCHS` epochs before the effective one. The effective epoch trails the latest by one during PoC, so a pass never runs ahead of the hosts.

The pass leaves alone a directory with no `epoch_<N>.db` (a session still opening), an escrow the registry still holds, and one whose row is active or carries a settle transaction still being reconciled. A parked escrow is removed like any other: by then the chain refuses its settlement, and with settlement disabled nothing would ever clear the row. A directory the pass cannot read or remove is logged (`session storage left in place`) and skipped, so it does not hold back the rest; it is tried again next epoch. A pass that cannot list the rows or the storage dir, or that runs past a minute, is logged and runs again on the next snapshot, picking up where it stopped.

The pass stops with the gateway. A value below the hosts' horizon is raised to it with a warning; `0` turns the pass off.

## One process per escrow set

The gateway is one process per set of escrows. It keeps its state in its own SQLite store under `DEVSHARD_STORAGE_DIR` and shares it with no other process: two gateways given the same escrows, through `DEVSHARDS_JSON` or the admin API, would each sequence their nonces and each ledger would see only its own share, which the chain rejects as diverged state. The Postgres settings (`PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PG_CONNECT_TIMEOUT`) are not read; when any is set the gateway logs `legacy Postgres settings are ignored` at boot, naming them (`env.LegacyPostgresVariablesSet`).

## Outbound dependencies

Besides the chain, two request checks fetch files from `huggingface.co`, so a gateway that serves chat needs egress to it.

### Stop token ids

A request's `stop_token_ids` are checked against the vocabulary the chain pins for the routed model in the current epoch, the same size the executor and the validator bound token ids by (`app/stop_token_vocabulary.go`). The gateway reads the model's Hugging Face repo and commit from the epoch's model snapshot on chain and the `vocab_size` from that commit's `config.json` on the Hugging Face hub, so a gateway that accepts `stop_token_ids` needs egress to `huggingface.co`.

A resolved size is cached for good, since a pinned commit never changes. The first request for a model in an epoch waits for the lookup, up to 10 seconds; a failed lookup is retried after 10 minutes, and until then every request with `stop_token_ids` for that model is refused.

The gateway looks the size up only for a model one of its escrows serves (`api/routes.go`, `servedModelVocabulary`), so a client naming arbitrary models cannot grow the cache or reach the chain; for any other model the field is refused like an unknown size. Until the first epoch is known, or while the size cannot be resolved, a request carrying `stop_token_ids` is refused with a 400 rather than forwarded with an id nobody proved safe.

A request without them never triggers the lookup. See [`filters/README.md`](../filters/README.md), "Stop token ids".

### The context check

The context check refuses, before any nonce exists, a chat request whose prompt and `max_tokens` its model's context cannot hold; what it counts and refuses is in [request.md](./request.md), "Step 6 in detail: the context check". It counts the prompt with the `tokenizer.json` of the Hugging Face repo and commit the epoch's model snapshot pins (`app/prompt_tokens.go`), so a gateway with a chain needs egress to `huggingface.co` for the check, as for `stop_token_ids`.

Each served model's file, up to 64 MiB (`tokenizer/library.go`, `maxTokenizerBytes`), is fetched once per pinned commit, in the background, on the first request for the model; until it loads, and for a model whose commit ships no `tokenizer.json` the gateway reads (Kimi-K2.6 ships a tiktoken model), requests go to a host unchecked. A failed fetch is retried after 10 minutes; when the pin moved and the new file fails to load, the model is unchecked until it loads, rather than counted with the previous file.

The log reads `prompt tokenizer loaded` on a load, `model has no tokenizer the gateway reads` for a pin that is not checked, `prompt tokenizer parse panicked` (an error, also not checked until the pin moves) for a file whose parse panicked, `prompt tokenizer could not be loaded` on a failed fetch, and `prompt tokenizer pin could not be read` when the chain query fails, in which case a tokenizer already loaded keeps counting.
