# devshardctl

## Build

```bash
go build -o devshardctl ./cmd/devshardctl/
```

Local HTTP proxy that exposes an OpenAI-compatible API for devshard inference.
Users point any OpenAI client at `localhost:8080` and make chat completion requests; the proxy handles all devshard protocol complexity internally.

## Configuration

All settings can be passed as flags or environment variables. Flags take precedence over env vars. Join-stack deploy template:
`deploy/join/config.devshard.env.template`.

### Chain and phase endpoints

| Env var | Default | Role |
| ------ | ------ | ------ |
| `DEVSHARD_CHAIN_GRPC` | `localhost:9090` | Chain gRPC URL for queries and unordered create/settle transactions |
| `DEVSHARD_CHAIN_RPC` (Optional) | `http://localhost:26657` | CometBFT RPC URL used as a fallback when gRPC is unreachable |
| `DEVSHARD_PUBLIC_API` (Optional) | `http://localhost:9000` | Public API URL for epoch/PoC phase and participants (cached). Set to `none` or `disabled` to query the chain directly |
| `DEVSHARD_PARAMS_SOURCE` (Optional) | `adaptive` | Set to `chain` to poll runtime params from the blockchain; otherwise, queries the `api` container (NodeManager gRPC port `:9400`) with chain fallback |

### Other settings

| Flag | Env var | Required | Default | Description |
| ------ | ------ | ------ | ------ | ------ |
| `--private-key` | `DEVSHARD_PRIVATE_KEY` | yes | - | Hex-encoded secp256k1 private key |
| `--escrow-id` | `DEVSHARD_ESCROW_ID` | yes | - | On-chain escrow ID |
| `--chain-grpc` | `DEVSHARD_CHAIN_GRPC` | no | `localhost:9090` | See above |
| `--model` | `DEVSHARD_MODEL` | no | `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` | Default model (used when request omits `model`) |
| `--port` | `DEVSHARD_PORT` | no | `8080` | Listen port |
| `--storage-path` | `DEVSHARD_STORAGE_PATH` | no | `~/.cache/gonka/devshard-<escrow-id>.db` | SQLite path for crash recovery |
| - | `DEVSHARD_API_KEYS` | no | - | Comma-separated public API bearer keys |
| - | `DEVSHARD_ADMIN_API_KEY` | no | - | Admin bearer key for finalize and `/v1/admin/*` endpoints |
| - | `DEVSHARD_CHAIN_ID` | no | `gonka-mainnet` | Chain ID for signing escrow create/settle txs (`gonka-mainnet`, `gonka-testnet`, `gonka-test`) |
| - | `DEVSHARD_TX_FEE_AMOUNT` | no | `1000000` | Fee amount for admin-created escrow transactions |
| - | `DEVSHARD_TX_FEE_DENOM` | no | `ngonka` | Fee denom for admin-created escrow transactions |
| - | `DEVSHARD_TX_GAS_LIMIT` | no | `500000` | Fallback gas limit for admin-created escrow and settlement transactions |
| - | `DEVSHARD_TX_POLL_TIMEOUT_MS` | no | `45000` | How long to wait for the create-escrow transaction result |
| - | `DEVSHARD_GATEWAY_DISABLED` | no | `false` | Return a 308 redirect-shaped JSON response for all non-admin requests |
| - | `DEVSHARD_GATEWAY_DISABLED_MESSAGE` | no | `please use ... base url` | Message shown while the gateway is disabled |
| - | `DEVSHARD_GATEWAY_DISABLED_NEW_URL` | no | - | Replacement chat completions URL returned while the gateway is disabled |
| - | `DEVSHARD_ESCROW_ROTATION_ENABLED` | no | `false` | Enable automatic epoch and depletion escrow rotation |
| - | `DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED` | no | `false` | Enable automatic finalization and on-chain settlement for rotated escrows |
| - | `DEVSHARD_ESCROW_ROTATION_PRE_POC_BLOCKS` | no | `300` | Blocks before the next epoch switch at `set_new_validators` to create temp bridge escrows |
| - | `DEVSHARD_ESCROW_ROTATION_MODELS_JSON` | when rotation enabled | - | JSON array of per-model rotation configs: `model_id`, `temp_count`, `target_count`, `amount`, `private_key_env` |
| - | `DEVSHARD_META_DRAIN_TIMEOUT_SECONDS` | no | `30` | After client disconnect, keep draining host SSE for protocol completion (`devshard_meta`, `ProcessResponse`, `MsgFinishInference`) up to this many seconds |
| - | `PGHOST` | no | - | When set (or `DEVSHARD_STORAGE_MODE` is `hybrid`/`postgres`), gateway settings and epoch accounting use Postgres only. Local `{baseStorageDir}/gateway.db` / `accounting.db` are migration sources, not runtime fallbacks |
| - | `PGPORT` | when `PGHOST` set | `5432` | Postgres port |
| - | `PGDATABASE` | when `PGHOST` set | - | Postgres database name |
| - | `PGUSER` | when `PGHOST` set | - | Postgres user |
| - | `PGPASSWORD` | when `PGHOST` set | - | Postgres password |
| - | `PG_CONNECT_TIMEOUT` | when `PGHOST` set | `2s` | Dial/auth timeout for each new Postgres connection (`common/storage/pgtimeouts`) |
| - | `PG_OPERATION_TIMEOUT` | when `PGHOST` set | `2s` | Per-call Go context deadline for gateway/accounting Postgres ops; `0` disables. Does **not** disable server-side `statement_timeout` / `lock_timeout` |
| - | `PG_IMPORT_TIMEOUT` | when `PGHOST` set | `5m` | Boot-time SQLite→Postgres import (+ leftover journal drain) budget |
| - | `PG_RETRY_INTERVAL` | when `PGHOST` set | session/payload defaults | Used by **session** / payload hybrid reconnect; gateway/accounting do not fall back to SQLite |
| - | `DEVSHARD_ACCOUNTING_WRITER_ID` | **required for HA** | hostname | Names this instance's epoch accounting rows in Postgres. **Multi-instance gateway deployments must set a stable, unique value per replica** (pod name / StatefulSet ordinal). Defaulting to hostname is only safe when hostnames are unique and stable across restarts; colliding ids make two replicas rewrite the same request-local rows |

Postgres timeout defaults live in `common/storage/pgtimeouts` and are shared by
gateway, epoch accounting, session storage, and payload reconnect. Two bounds
are **not** env-tunable — they are applied as connection `RuntimeParams` on every
pooled connection:

| Server param | Default | Meaning |
| --- | --- | --- |
| `statement_timeout` | `5s` | Abort one SQL statement that runs too long |
| `lock_timeout` | `3s` | Abort a statement waiting too long for a row/table lock |

### Gateway persistence backend

Gateway settings, devshard topology, rotation status, escrow commitments, suspicious
hosts, and participant throttle state are persisted in Postgres when `PGHOST` is
set (or `DEVSHARD_STORAGE_MODE` is `hybrid` / `postgres`). Local
`{baseStorageDir}/gateway.db` remains only as a one-shot migration source.
Full design (including HA LWW-per-key semantics vs additive accounting) is in
[storage-design.md](storage-design.md#gateway-store).

When Postgres is selected:

- On startup Postgres must be reachable or open fails (no SQLite runtime fallback).
- Existing SQLite data is imported once (idempotent; skipped when Postgres already
  has settings or a migration marker exists).
- Any leftover `gateway_sync_journal` rows from older hybrid builds are drained,
  then the gateway serves **Postgres only**.

When `PGHOST` is unset and mode is `sqlite`/`auto`, only SQLite is used.

**Rollback:** unset `PGHOST` and use `DEVSHARD_STORAGE_MODE=sqlite` to run
SQLite-only again. Writes that landed in Postgres after migration are not
automatically copied back to SQLite.

**Postgres outage:** gateway store and epoch accounting fail closed (errors /
boot failure). Session storage may still degrade to owner-only SQLite while
reconnecting — that fallback is session-only.

Gateway tables (`gateway_*`, `escrow_rotation_*`, `participant_throttle_state`)
can share the same Postgres database as devshard session or payload tables; table
names do not collide.

### Epoch accounting backend

Epoch accounting follows the same switch, with one difference: it is written to
merge across instances rather than replaced wholesale. Every escrow is stored as
rows in `accounting_escrow_*` with an explicit merge rule per field, so two
gateways that both hold an escrow combine instead of overwriting each other, and
a retried flush cannot double count. See
[storage-design.md](storage-design.md#epoch-accounting) for the full field table.

**HA requirement.** When more than one gateway can write accounting against the
same Postgres database, each replica **must** set
`DEVSHARD_ACCOUNTING_WRITER_ID` to a stable, unique string. Request-local counters
are partitioned by that id; two replicas that share it overwrite each other's
share. SQLite mode ignores the variable (single writer, no merge). Resolution
order: env → `os.Hostname()` → `"default"`.

On first boot against a database written by an older build, the legacy
`accounting_escrows` blob table is converted into these rows once (marker
`blob_to_rows`) under the frozen writer `_legacy_blob`, then drained. A separate
`sqlite_import` marker covers one-shot import from local `accounting.db`.

#### HA merge framework

There is no single merge rule that fits every field, so each one is classified
first. **The question that decides the class: if two instances are live on the
same escrow, do both of them produce this value?**

| Class | Test | Merge rule | Storage shape |
| --- | --- | --- | --- |
| Request-local | Only the instance that dispatched the request can produce it — it needs a local signal (`RecordGhost`, `RecordRealSend`, `RecordUsage`) | `SUM` across writers | Per-writer row `(escrow_id, writer_id, key)` holding that writer's own share |
| Replicated | Read off the committed diff stream or a verdict, so every instance derives it identically | Set union; monotonic flags with `OR` | One row per nonce, **no** `writer_id`; totals counted on read |
| Absolute mirror | The chain already publishes the total and each instance re-reads it | `GREATEST` per column | Shared row, no `writer_id` |
| Ordered / identity | Moves through a fixed order, or is fixed at registration | Highest rank wins / identity write | Shared row |

Worked examples: a `finished_used` disposition is request-local (a passive
instance sees the finish on chain but never learns the result was used). A
protocol-only nonce is replicated (it is exactly the absence of a local start).
Host stats and the `latest` watermark are absolute mirrors. Escrow phase is
ordered; slots and timeouts are identity.

**The rule for replicated facts is the counter-intuitive one: never store a
count.** Two instances observing one chain event would each contribute 1, and
summing reports 2; taking the max instead reports 1 but silently drops the second
nonce when the two instances observed *different* events. Storing the nonce
itself sidesteps the choice — the row is the observation, so a union is exact in
both directions.

Whatever the class, three invariants hold:

- **Replay-safe.** A flush may be replayed after an ambiguous timeout. Summed
  rows are written as absolute values, set rows are insert-if-absent; never
  `count = count + delta`.
- **No cross-writer writes.** An instance only ever writes rows it owns, or adds
  set members. This is what removes the need for a lease or fencing token.
- **Monotonic.** Nothing decreases. A resolved challenge is flagged, not deleted,
  so a repeated verdict from another instance cannot reopen it.

#### Challenges and the legacy `ChallengeBySlot` carry

A challenge is a protocol fact: inference status `Challenged` / `Validated` /
`Invalidated` on a committed nonce, attributed to the executor slot. The tracker
records it in `Challenge map[nonce]{Slot, Resolved}`:

- `ProtocolChallenged` → `openChallenge` (insert once; never reopen a resolved entry)
- `ProtocolValidated` → `resolveChallenge` (`Resolved = true`, entry kept)
- `ProtocolInvalidated` → resolve, then add the nonce to `Invalid`

`UnresolvedChallenges` in the query API is the count of set members with
`!Resolved` (plus any frozen legacy baseline below). Resolved entries stay in the
set so a repeated challenge verdict cannot reopen them and so HA can merge with
`resolved OR excluded.resolved`.

**Why `ChallengeBySlot` existed.** The API wanted a per-slot number. The old
design persisted that number (`ChallengeBySlot[slot]++` / `--`) and kept
`OpenChallenge` only in memory (`json:"-"`) so close knew which slot to
decrement. That was cheap for single-writer reporting, but the open set was lost
on every restart (counts drifted up) and a bare count cannot merge across HA
writers.

**What changed.** The hot path no longer writes `ChallengeBySlot` /
`InvalidBySlot`. Those maps, and `InvalidLegacy`, are **read-only carries** for
blobs written before the set layout. New activity — SQLite or Postgres — only
touches the sets. The same is true if you never leave SQLite: after upgrade, new
escrows leave the carry empty.

**What we do not lose for new data.** The reportable fields
(`UnresolvedChallenges`, recorded invalid) are unchanged in meaning; they are
derived on read instead of stored as counters. The tradeoff is a little more
CPU/memory (count the set; keep resolved nonces until the escrow is pruned), not
missing API data. Correctness improves: restarts and multi-writer merge stay exact.

**When the carry can leave the codebase.** After every deployment you care about
has migrated past the old blob shape, no old `accounting.db` is still imported,
and retention has dropped every escrow that still holds non-zero carry (or those
rows have been verified empty). Until then, keep decode + fold of the old JSON /
`accounting_escrow_slot_counts` paths. Dropping the carry too early loses
historical totals that cannot be reconstructed (nonces were never stored).

#### Adding a field

1. Classify it with the table above, and add it to the field table in
   `storage-design.md`.
2. Add it to `escrowState` in `accounting/tracker.go`. For a replicated fact,
   store the set (nonce → slot), not a count.
3. If the query path needs a per-slot or per-key total, derive it in
   `escrowState.view` rather than storing it as well. Storing both a set and its
   count means two representations under two merge rules, which drift apart and
   cannot be reconciled afterwards.
4. Extend `escrowBlob` in `accounting/store.go` plus `blobFromEscrow` /
   `applyLoadedEscrow`. This is also the SQLite format, so leave old field names
   and JSON tags in place and treat them as read-only carries.
5. In `accounting/store_postgres_rows.go`: add the DDL to `accountingRowSchema`
   (new tables via `CREATE TABLE IF NOT EXISTS`, new columns via
   `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` with a default that identifies
   pre-existing rows), then the read in `readLedger`, the write in
   `writeEscrowRows`, and the table in `deleteEscrowRows` so retention pruning
   removes it.
6. For a summed field, feed `contributionFromBlob` and the peer baseline so the
   writer publishes its own share; for a set, no baseline is needed.
7. Test both HA directions, not just the sum: two writers observing *different*
   things must keep both, and two writers observing the *same* thing must count it
   once. `accounting/merge_test.go` covers the rules without Docker;
   `store_postgres_ha_test.go` covers them against a real database.

Only the merge machinery is Postgres-specific. The in-memory model and the blob
format are shared with SQLite, which has a single writer and therefore never
merges anything.

#### Staying on SQLite: how new fields appear

SQLite does **not** run the row-merge schema. It keeps one JSON blob per escrow
in `accounting_escrows.payload` (`store_sqlite.go`). Adding a field while
remaining on SQLite is therefore a **blob evolve**, not a SQL migration:

1. Add the Go field to `escrowState` / `escrowBlob` with a new JSON key
   (`omitempty` for optional data).
2. On load, `encoding/json` leaves missing keys as zero values — old files open
   without a rewrite step.
3. On the next successful flush, the whole blob is rewritten with the new keys
   present. There is no per-column `ALTER`, no marker table, and no
   `writer_id`.
4. Keep old JSON tags as read-only carries when a representation changes (e.g.
   pre-set `challenge_by_slot` / `invalid_nonces`), so a file written by an older
   build still loads and does not double-count after promotion into a set.
5. Bump `SchemaVersion` in `types.go` when the query API shape changes; it is
   stored in `accounting_meta` for observability, not as a gate that blocks load.

If you later switch that node to Postgres, the same blob is what
`sqlite_import` / `blob_to_rows` convert — so the SQLite blob rules and the
Postgres field checklist must stay aligned.


```bash
devshardctl \
  --private-key "deadbeef..." \
  --escrow-id 42 \
  --chain-grpc "localhost:9090"

# In another terminal:
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}'
```

Or using environment variables:

```bash
export DEVSHARD_PRIVATE_KEY="deadbeef..."
export DEVSHARD_ESCROW_ID="42"
export DEVSHARD_CHAIN_GRPC="localhost:9090"

devshardctl
```

## Finalize Escrow

```bash
curl -X 'POST' http://localhost:8080/v1/finalize \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  > ./settle.json
```

This top-level endpoint is for a single-devshard gateway and returns settlement
JSON only. On a multi-devshard gateway, use the per-devshard route or the admin
settle route below.

## Settle Escrow On Chain

```bash
curl -X POST http://localhost:8080/v1/admin/devshards/42/settle \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"private_key_env":"DEVSHARD_PRIVATE_KEY"}'
```

The admin settle endpoint locally deactivates the devshard, finalizes it if
needed, signs `MsgSettleDevshardEscrow`, and broadcasts the transaction.

## Endpoints

### GET /v1/models

Lists the models currently advertised by the devshard gateway. The response
uses the OpenAI list envelope and includes OpenRouter-style metadata fields
(`name`, `description`, `context_length`, `architecture`, `pricing`,
`top_provider`, and `supported_parameters`) where the gateway can provide
stable values.

```json
{
  "object": "list",
  "data": [
    {
      "id": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
      "object": "model",
      "owned_by": "gonka",
      "name": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
    }
  ]
}
```

### POST /v1/chat/completions

Standard OpenAI chat completion format. The full request body is forwarded as the inference prompt.

Request fields used by the proxy:

- `model` -- passed to InferenceParams (falls back to `DEVSHARD_MODEL`)
- `max_tokens` / `max_completion_tokens` -- passed to InferenceParams. If neither is set, the gateway adds `max_tokens` using `default_request_max_tokens` (default `3072`). If one is set above `request_max_tokens_cap` (default `4096`), the gateway caps it before forwarding. Both values can be overridden per model inside `model_limits`.
- `stream` -- if true, response is SSE; if false, response is a single JSON object

Returns 429 if another inference is already in flight.

### POST /v1/finalize

Admin endpoint. Triggers devshard finalization and returns settlement JSON.

No request body needed. Response is the settlement payload ready for `inferenced tx inference settle-devshard-escrow`.
For multi-devshard gateways, call `/devshard/{id}/v1/finalize` for manual
payload generation, or prefer `/v1/admin/devshards/{id}/settle` to finalize and
broadcast the settlement in one step.

### GET /v1/status

Returns current session state.

```json
{
  "escrow_id": "42",
  "nonce": 15,
  "phase": "active",
  "balance": 5000000000,
  "config": {
    "refusal_timeout": 60,
    "execution_timeout": 1920,
    "token_price": 1,
    "create_devshard_fee": 10000,
    "fee_per_nonce": 1000,
    "vote_threshold": 8,
    "validation_rate": 1000,
    "inference_seal_grace_nonces": 160,
    "inference_seal_grace_seconds": 3600
  }
}
```

Phase values: `active`, `finalizing`, `settlement`.

`config` mirrors the session's frozen `SessionConfig`, including the paired seal-grace gates (`inference_seal_grace_nonces`, `inference_seal_grace_seconds`).

### GET /v1/state

Admin endpoint. Returns the full session state and requires
`Authorization: Bearer $DEVSHARD_ADMIN_API_KEY`.

### POST /v1/admin/settings

Admin endpoint. Updates persisted gateway settings. Global request/token caps
remain the fallback, and `model_limits` overrides them per model before the
gateway applies the model's current capacity scale factor. `model_limits` also
controls per-model inference access with `access_mode`: `open`, `api_key`, or
`admin_only`. If a model has no `access_mode` configured, it defaults to
`admin_only`.

```bash
curl -X POST http://localhost:8080/v1/admin/settings \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "max_concurrent_requests": 20,
    "max_input_tokens_in_flight": 200000,
    "model_limits": [
      {
        "model_id": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
        "max_concurrent_requests": 20,
        "max_input_tokens_in_flight": 200000,
        "access_mode": "api_key"
      },
      {
        "model_id": "moonshotai/Kimi-K2-Instruct",
        "max_concurrent_requests": 8,
        "max_input_tokens_in_flight": 80000,
        "access_mode": "admin_only",
        "access_message": "Kimi is temporarily unavailable"
      }
    ]
  }'
```

When a model uses `access_mode: "api_key"`, `/v1/chat/completions` and
`/devshard/{id}/v1/chat/completions` require either a configured
`DEVSHARD_API_KEYS` bearer token or the admin bearer token. When a model uses
`access_mode: "admin_only"`, only the admin bearer token can run inference.
Set `access_mode: "open"` to allow unauthenticated inference for that model:

```bash
curl -X POST http://localhost:8080/v1/admin/settings \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model_limits":[{"model_id":"moonshotai/Kimi-K2-Instruct","access_mode":"open"}]}'
```

`/v1/status` is always public and reports `access_mode`, `access_enabled`,
`active_devshards`, `routable_devshards`, and `routable` per model. Access mode
does not zero `current_weight`, `scale_factor`, or limiter caps; those values
continue to reflect effective gateway capacity.

### POST /v1/admin/escrows

Admin endpoint. Creates a new on-chain devshard escrow by signing
`MsgCreateDevshardEscrow` locally and broadcasting over chain gRPC, with
CometBFT RPC fallback when gRPC is unreachable. By default, the returned
escrow ID is also registered as an active local gateway runtime.

```bash
curl -X POST http://localhost:8080/v1/admin/escrows \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"amount":5000000000,"model_id":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8","private_key_env":"DEVSHARD_PRIVATE_KEY"}'
```

Set `"register": false` to create the escrow on-chain without adding it to the
local runtime pool.

### GET /v1/admin/devshards/{id}/participants

Admin endpoint. Returns the participant host keys in a devshard escrow and the
reactive throttle state used by gateway routing.

```bash
curl http://localhost:8080/v1/admin/devshards/42/participants \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY"
```

Each participant entry includes `participant_key`, `slot_count`, `tracked`,
`quarantined`, `quarantine_mode`, `model_ids`, `shadow_quarantined`,
`probe_quarantined`, `probationary`, `blocked`, `request_allowed`,
`available_for_capacity`, `tokens`, `burst`, `failure_strikes`, and, when quarantined,
`quarantine_until` and `quarantine_remaining_ms`. `blocked` means probe
quarantine or token exhaustion would reject a real host call now. Shadow
quarantine and probation still send real attempts, but the host is treated as
no-winner for the affected model. `model_ids` lists the models affected by the
automatic quarantine row; an empty list means legacy/global state.
`failure_strikes` is the unified per-model soft-failure/probation counter.

### POST /v1/admin/devshards/{id}/settle

Admin endpoint. Locally deactivates the devshard, finalizes it if it is not
already in settlement phase, signs `MsgSettleDevshardEscrow`, and broadcasts the
signed transaction over chain gRPC (CometBFT RPC fallback).

```bash
curl -X POST http://localhost:8080/v1/admin/devshards/42/settle \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"private_key_env":"DEVSHARD_PRIVATE_KEY"}'
```

If the request body omits `private_key` and `private_key_env`, the gateway uses
the key already persisted for that devshard. The endpoint returns `409` while
the devshard still has active requests.

### Automatic escrow rotation

Automatic rotation uses two roles:

- `regular` escrows carry normal traffic for an epoch.
- `temp` escrows are bridge escrows that keep capacity available through the
  PoC/epoch transition.

When `escrow_rotation.enabled` is true, the gateway watches the chain phase
snapshot (public API when configured, otherwise chain gRPC/RPC) and also
replaces escrows that approach the low-balance or high-nonce limits. When it is
false, both epoch rotation and depletion replacement are disabled.

1. During inference phase, when the chain is within `pre_poc_blocks` of PoC,
   the gateway ensures `temp_count` temp escrows exist for the current epoch.
2. It then locally deactivates active non-temp escrows, finalizes them, and
   settles them on-chain through chain gRPC (CometBFT RPC fallback).
3. After the next epoch leaves PoC, it ensures `target_count` regular escrows
   exist for the new epoch.
4. It then deactivates, finalizes, and settles the previous epoch's temp
   escrows.

Set `escrow_rotation.settlement_enabled` to `false` to keep automatic creation
and local deactivation while skipping automatic finalization and on-chain
settlement. Manual settlement through `POST /v1/admin/devshards/{id}/settle`
remains available.

Rotation settings are persisted in `gateway.db`. After first boot, update them
through `POST /v1/admin/settings`:

```bash
curl -X POST http://localhost:8080/v1/admin/settings \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "escrow_rotation": {
      "enabled": true,
      "settlement_enabled": false,
      "pre_poc_blocks": 300,
      "models": [{
        "model_id": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
        "temp_count": 8,
        "target_count": 16,
        "amount": 5000000000,
        "private_key_env": "DEVSHARD_PRIVATE_KEY"
      }]
    },
    "tx_gas_limit": 700000
  }'
```

`tx_gas_limit` is persisted in `gateway.db` and used by automatic escrow
rotation for both create and settle transactions. A per-request `gas_limit` on
`POST /v1/admin/escrows` or `POST /v1/admin/devshards/{id}/settle` still takes
precedence. If `tx_gas_limit` is `0`, the gateway falls back to
`DEVSHARD_TX_GAS_LIMIT` and then the built-in default.

### Gateway disabled state

Set `DEVSHARD_GATEWAY_DISABLED=true` on first boot, or update
`disabled.enabled` through `POST /v1/admin/settings`, to make the gateway return
a redirect-shaped JSON response for every non-admin request:

```json
{"status":308,"message":"please use https://.../v1/ base url","new_url":"https://.../v1/chat/completions"}
```

The disabled settings are persisted in `gateway.db`:

```bash
curl -X POST http://localhost:8080/v1/admin/settings \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"disabled":{"enabled":true,"message":"please use https://.../v1/ base url","new_url":"https://.../v1/chat/completions"}}'
```

### GET /metrics

Prometheus scrape endpoint. In the join-stack deployment, `devshardctl` is
published only on the host loopback address, so scrape it directly from the host:

```yaml
scrape_configs:
  - job_name: devshardctl
    static_configs:
      - targets: ["127.0.0.1:18080"]
```

Do not expose `/metrics` through the public nginx gateway. Public devshard
clients should use `/devshard-gateway/v1/...`; Prometheus should use
`http://127.0.0.1:18080/metrics`.

## OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="unused")
response = client.chat.completions.create(
    model="Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
    messages=[{"role": "user", "content": "Hello"}],
    max_tokens=100,
)
print(response.choices[0].message.content)
```

The `api_key` is required by the SDK. It is ignored for models with
`access_mode: "open"` and must match one of `DEVSHARD_API_KEYS` for models with
`access_mode: "api_key"`. Models with `access_mode: "admin_only"` require
`DEVSHARD_ADMIN_API_KEY`.

### Standalone deployment

Gateway beside a read-only full node (no edge-api / dapi):

```bash
export DEVSHARD_PUBLIC_API=none          # phase checks via chain
export DEVSHARD_PARAMS_SOURCE=chain      # runtime params via chain
export DEVSHARD_CHAIN_GRPC=node:9090
# export DEVSHARD_CHAIN_RPC=http://node:26657   # optional; derived if omitted
```

## Finalization and settlement

After all inferences are done:

1. POST to `/v1/admin/devshards/{id}/settle` with `Authorization: Bearer $DEVSHARD_ADMIN_API_KEY` and a signing key such as `{"private_key_env":"DEVSHARD_PRIVATE_KEY"}`.
2. The gateway locally deactivates the devshard, runs finalization if needed, collects host signatures, and broadcasts `MsgSettleDevshardEscrow` on-chain.

The proxy holds the session open until finalization. Once finalized, the session cannot accept new inferences.

## Non-streaming vs streaming

Non-streaming (`"stream": false` or omitted): the proxy buffers all SSE chunks from the ML node and returns the final assembled JSON response.

Streaming (`"stream": true`): the proxy relays SSE `data:` lines in real time. The stream ends with `data: [DONE]`. Devshard protocol events (receipts, metadata) are filtered out -- only inference data reaches the client.

If the client disconnects before the host finishes, the proxy keeps draining the host SSE stream in the background for up to `DEVSHARD_META_DRAIN_TIMEOUT_SECONDS` (default 30s) so protocol completion (`devshard_meta`, `ProcessResponse`, `MsgFinishInference`) can still run. Further writes to the disconnected client are swallowed.

## Speculative execution

The proxy uses speculative execution to reduce tail latency and route around unresponsive hosts.

See `devshard/docs/speculative-proxy.md` for the detailed design and escalation rules.
