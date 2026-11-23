# Runbook

Step-by-step procedures for running the gateway: releasing, rolling back, backing up, settling and deactivating escrows, and finding the cause of a symptom. Every command is run against a live gateway; the keys they set are described in [configuration.md](./configuration.md) and the signals they read in [observability.md](./observability.md).

## Before you start

The commands below use two shell variables: `GATEWAY`, the base URL of the gateway (`http://localhost:18080` on the [`deploy/`](../deploy/README.md) stack), and `ADMIN_KEY`, the value of `DEVSHARD_ADMIN_API_KEY`. The `docker compose` commands run from the `deploy` directory.

```bash
export GATEWAY=http://localhost:18080
export ADMIN_KEY=<the value of DEVSHARD_ADMIN_API_KEY>
```

## When something is wrong

| Symptom | Look at |
| --- | --- |
| every request 429 | `limiter_queue_depth` and `effective_max_concurrent_requests` — the weight model may have scaled capacity to almost nothing after an epoch switch |
| every request 503 with no model listed | the chain snapshot is stale, or the escrow set is empty — check `chain_snapshot_healthy` and `/v1/admin/devshards` |
| 503 on a healthy-looking gateway | `buffered_response_bytes` at the ceiling: non-streaming replies are holding the whole budget |
| a model returns 401 to every caller but the admin | neither the model nor the network names an `access` for it — set `access` under `models.<id>`; on an `api_key` model, a caller whose key is not an active client key is refused too, so list them with `GET /v1/admin/api-keys` |
| the admin surface 404s | `DEVSHARD_ADMIN_API_KEY` is unset |
| burns climbing | `ghost_nonces_burned_total` by reason — see [accounting.md](./accounting.md) |
| prompts past a model's context reach hosts | the log line `prompt tokenizer could not be loaded`, `prompt tokenizer pin could not be read`, `model has no tokenizer the gateway reads` or `prompt tokenizer parse panicked` for the model, or no context length for it — see [operations.md](./operations.md), "The context check" |
| shutdown reports "abandoned with work still running" | a host stopped answering and the drain hit the grace period; the votes it owed were not paid |

To reproduce an escrow lifecycle problem (create, drain, timeout votes, settle, prune) without a stand, use the scenario tests in [`scenarios/`](../scenarios/README.md); the `loadsim` test described in [CONTRIBUTING.md](../CONTRIBUTING.md#load-simulation) stays the profiling tool.

## Release and rollback

A release is an image. `make release` builds it for `linux/amd64` (`PLATFORM`) and pushes it as `$(GATEWAY_IMAGE):<tag>` for every tag in `GATEWAY_TAGS`. It refuses to run without `GATEWAY_VERSION`, which is stamped into every settlement ([`CONTRIBUTING.md`](../CONTRIBUTING.md), "Before you push"), and without `GATEWAY_TAGS`.

To release and deploy:

1. Build and push the image from `devshard/cmd/gateway`:

   ```bash
   make release GATEWAY_VERSION=<version> GATEWAY_TAGS='<tag> <tag>-latest'
   ```

2. Point the stack at the new tag and recreate the container from the `deploy` directory. `GATEWAY_IMAGE` is a full image reference with its tag:

   ```bash
   export GATEWAY_IMAGE=<registry>/<image>:<tag>
   docker compose -f docker-compose.devshard-gateway.yml pull
   docker compose -f docker-compose.devshard-gateway.yml up -d
   ```

   `up -d` stops the running container with SIGTERM, so the outgoing gateway runs its whole shutdown ([operations.md](./operations.md), "Shutdown") before the new one opens the same storage directory.

3. Check the new process: `curl -s "$GATEWAY/v1/status"`.

To roll back, set `GATEWAY_IMAGE` to the previous tag and repeat step 2. Before you do, read what the newer release wrote into the storage directory, because that decides what the older one reads:

- **`gateway.db`** is versioned by `schema_version`, one row per applied entry of `migrations` (`store/store.go`, `migrate`). The list is append-only, so a release reads every store a release with a list no longer than its own wrote, and migrates it up at `Open`. An older release opening a store a newer one migrated applies nothing and does not refuse it: the tables and columns it knows are all still there, and every column a later entry adds carries a default, so the rows it inserts stay valid. A rollback across a migration that is not a plain added table or column is not covered by this; read the entries the newer release added before rolling back over them.
- **`accounting.db`** carries the ledger's `accounting.SchemaVersion` and is read only by a release with the same number (`accounting/sqlload.go`, `Store.Load`). Across a change of that number the ledger starts without its stored counters, logs `nonce accounting started without its stored counters`, and replaces the stored tables on its next write. That loses the ledger's history, never money: the ledger is observation, and settlement reads the escrow sessions ([accounting.md](./accounting.md), "Storage").
- **The escrow sessions** (`escrow-<id>/epoch_<N>.db`; [operations.md](./operations.md), "Session storage") are written by the devshard libraries the release is built with, and a gateway release runs only with the devshard release of the same version.

## Backup and restore

The storage directory (`DEVSHARD_STORAGE_DIR`, mounted from `DEVSHARD_STORAGE_HOST_DIR`) holds everything the gateway remembers: `gateway.db` (escrow rows, create commitments, admin overrides, client API keys), `accounting.db` (the nonce ledger) and one `escrow-<id>/` directory per escrow session. The three describe the same escrows at the same moment, so they are archived together and restored together: a `gateway.db` older than the session directories beside it lacks the rows of escrows those sessions serve, and session directories older than the rows hold nonces the hosts have moved past. No command takes a backup for you; this is the manual procedure.

1. **Stop routing.** Turn on the kill switch ([operations.md](./operations.md), "The kill switch") through the admin settings: `curl -X POST -H "Authorization: Bearer $ADMIN_KEY" -d '{"disabled": true}' "$GATEWAY/v1/admin/settings"`. Clients are refused from then on; the admin and recovery surfaces stay up.
2. **Wait for the drain.** `devshard_gateway_inflight_requests` reaches 0 once the last client has its answer, and `devshard_gateway_owed_timeout_votes` reaches 0 once every vote the races owe is posted, which can take as long as the protocol's own deadline, half an hour after a host confirmed the work ([race.md](./race.md), "The timeout-vote queue").
3. **Stop the container.** `docker compose -f docker-compose.devshard-gateway.yml stop` runs the shutdown ([operations.md](./operations.md), "Shutdown"): the ledger's final write, the sessions' close and the store's close. The escrow tick and the ledger's sweep write to the storage directory while the process runs, so it is copied only once the process is down.
4. **Checkpoint the WAL.** Both databases run in WAL mode (`connectionPragmas` in `store/store.go` and in `accounting/sqlstore.go`). A clean close folds the WAL back into the database; where a `gateway.db-wal` or `accounting.db-wal` file is still there, fold it in before copying: `sqlite3 gateway.db 'PRAGMA wal_checkpoint(TRUNCATE);'`, and the same for `accounting.db`.
5. **Archive the whole directory in one piece**, for example `tar -C "$DEVSHARD_STORAGE_HOST_DIR" -czf gateway-storage-<date>.tar.gz .`, so `gateway.db`, `accounting.db` and every `escrow-<id>/` come from the same moment.
6. **Start again** with `docker compose -f docker-compose.devshard-gateway.yml up -d`; the admin overrides are in the store, so the kill switch is still on. Turn it off with `{"disabled": false}` through the same endpoint.

To restore, stop the container, replace the contents of the storage directory with one archive's contents (never files from two archives), and start it with the kill switch still on from the archived overrides; check `/v1/status` and the escrow list before turning it off. Two rules decide what a restored store does at boot ([operations.md](./operations.md), "Boot", steps 4 and 5):

- **`DEVSHARDS_JSON` writes rows back.** An escrow settled after the archive was taken has its row restored as active, and the seed writes back every seeded escrow whose row settlement deleted. Neither is served: publication asks the chain, refuses an escrow it reports settled (`bridge.ErrEscrowSettled`) and deletes its row again, so a restore cannot resurrect a settled escrow.
- **What happened after the archive is not in it.** An escrow created after the archive was taken has no row in it, so the restored gateway does not know it; an escrow that served after the archive comes back with its archived session, older than the copy its hosts hold. Take the archive right before the change it protects against, and settle or let drain what you do not need restored.

## The two routing lists

`participant_allowlist` narrows **which participants this gateway routes to**. `unthrottled_participants` names participants whose congestion window and ejection it declines to apply, and admits them whether or not the allowlist names them. Both are set through `/v1/admin/settings`, by `PUT` or `POST`, and nowhere else: there is no env value and no default behind them. Either can also be set for one model under `models.<id>`: the model's list replaces the network list for that model alone, and an empty list clears it for that model. `GET /v1/admin/state` shows the lists each model runs under in `participants`.

**That makes them one `PUT` away from being cleared.** A `PUT` replaces the stored overrides document whole, so a `PUT` that does not resend a list drops it, and a dropped narrowing fails open: the gateway routes to everybody again without refusing anything. The same is true of a fresh or lost store. A `POST` leaves a list it does not name in place. Read the lists back either way: the `settings written` audit line carries `participant_allowlist` and `unthrottled_participants` as they stand after the call, and each model's effective lists under `participants_by_model`.

An entry must look like an address. The shape is checked when the settings document is built, so a short host label pasted from a log or a Grafana `host=` tag is refused at the settings call rather than narrowing dispatch to nobody.

Neither list restrains anything but routing. Host pings, catch-up, finalize, the retirement flush, the timeout sweep and the warmup prober all address the group the chain gave the escrow. See [routing.md](./routing.md), "Paths that ignore the routing lists, by design".

## Settling an escrow that is still busy

`POST /v1/admin/devshards/{id}/settle` answers 409 `devshard busy` while requests are still spending the escrow's nonces, and for up to a minute after its timeout sweep failed a vote, so the vote is retried before finalize pays that record in full ([escrows.md](./escrows.md), "Settlement and retirement"). By then the escrow has already been parked and taken out of routing, so it is draining: calling again once it has drained settles it.

`POST /v1/admin/devshards/{id}/settle?force=true` does not wait:

```bash
curl -sS -X POST -H "Authorization: Bearer $ADMIN_KEY" "$GATEWAY/v1/admin/devshards/<id>/settle?force=true"
```

This route reads `force` from the query alone and takes no body; only the batch route below also reads it from its body. It crosses the busy check and nothing else — a second admin settle of the same escrow waits for the first to return and answers 404 `unknown devshard` if the first settled it, one the rotation lifecycle has in flight is still refused, and an escrow already settled is still not rebroadcast. **The requests still in flight are failed for their callers, and their full reserved cost is paid to the hosts that held them**, because finalize closes the door on new nonces and the records still live take the settlement default. The forced call is written to the audit log as `escrow settled under force`, with `in_flight` naming how many requests it overrode.

A forced settlement runs on an escrow that still has requests, so the one that finishes last would close the escrow's store while finalize is still collecting signatures. Finalizing, building the settlement, inspecting and the admin finalize and signature-collection routes therefore each take a settlement hold, and the draining escrow closes only after they return. The hold is counted apart from requests, so it neither makes the escrow busy nor moves routing (`registry/views.go`, `Registry.HoldSettlement`).

A settle that has started runs to its end even if the client leaves, because a settle cut short between its timeout sweep and the end of finalize would pay full reserve on the records the sweep did not reach: it runs on a context detached from the request, bounded by `adminSettleTimeout` (5 minutes; `api/admin_settle.go`). That timeout starts counting before the settle waits for the escrow's operator lock, and it does not interrupt that wait: a settle queued behind another write of the same escrow waits until that write returns, then has only what is left of the 5 minutes. A settle that does time out stops before finalize or partway through it, and the next settle resumes the finalize where it stopped.

## Settling an escrow the chain has pruned

The chain deletes an escrow a couple of epochs after the one that funded it. A settle of one it no longer holds answers 410 `escrow pruned on chain` and drops the escrow's row, so it is gone from the list afterwards; nothing is finalized or broadcast. See [escrows.md](./escrows.md), "Settlement and retirement".

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

## Creating an escrow the wallet cannot pay for

`POST /v1/admin/escrows` reads the wallet's spendable balance before it builds the transaction, and answers 402 naming the wallet, what it holds and what the amount plus the fee needs, without broadcasting anything.
