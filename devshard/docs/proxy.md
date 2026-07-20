# devshardctl

## Build

```bash
go build -o devshardctl ./cmd/devshardctl/
```

Local HTTP proxy that exposes an OpenAI-compatible API for devshard inference.
Users point any OpenAI client at `localhost:8080` and make chat completion requests; the proxy handles all devshard protocol complexity internally.

## Configuration

All settings can be passed as flags or environment variables. Flags take precedence over env vars.

| Flag | Env var | Required | Default | Description |
| ------ | ------ | ------ | ------ | ------ |
| `--private-key` | `DEVSHARD_PRIVATE_KEY` | yes | - | Hex-encoded secp256k1 private key |
| `--escrow-id` | `DEVSHARD_ESCROW_ID` | yes | - | On-chain escrow ID |
| `--chain-rest` | `DEVSHARD_CHAIN_REST` | no | `http://localhost:1317` | Chain REST API URL |
| `--model` | `DEVSHARD_MODEL` | no | `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` | Default model (used when request omits `model`) |
| `--port` | `DEVSHARD_PORT` | no | `8080` | Listen port |
| `--storage-path` | `DEVSHARD_STORAGE_PATH` | no | `~/.cache/gonka/devshard-<escrow-id>.db` | SQLite path for crash recovery |
| - | `DEVSHARD_API_KEYS` | no | - | Comma-separated public API bearer keys |
| - | `DEVSHARD_ADMIN_API_KEY` | no | - | Admin bearer key for finalize and `/v1/admin/*` endpoints |
| - | `DEVSHARD_CHAIN_ID` | no | `gonka-mainnet` | Chain ID for signing escrow create/settle txs. Known values: `gonka-mainnet` (production), `gonka-testnet` (devnet/testnet), `gonka-test` (testenv mock-chain). |
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

## Quick start

```bash
devshardctl \
  --private-key "deadbeef..." \
  --escrow-id 42 \
  --chain-rest "http://localhost:1317"

# In another terminal:
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}'
```

Or using environment variables:

```bash
export DEVSHARD_PRIVATE_KEY="deadbeef..."
export DEVSHARD_ESCROW_ID="42"
export DEVSHARD_CHAIN_REST="http://localhost:1317"

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
    "execution_timeout": 1200,
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
`MsgCreateDevshardEscrow` locally and broadcasting the signed transaction to
`DEVSHARD_CHAIN_REST` via `/cosmos/tx/v1beta1/txs`. By default, the returned
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
signed transaction to `DEVSHARD_CHAIN_REST`.

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
snapshot from `DEVSHARD_PUBLIC_API` and also replaces escrows that approach the
low-balance or high-nonce limits. When it is false, both epoch rotation and
depletion replacement are disabled.

1. During inference phase, when the chain is within `pre_poc_blocks` of PoC,
   the gateway ensures `temp_count` temp escrows exist for the current epoch.
2. It then locally deactivates active non-temp escrows, finalizes them, and
   settles them on-chain through `DEVSHARD_CHAIN_REST`.
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
