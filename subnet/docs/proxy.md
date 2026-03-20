# subnetctl

Local HTTP proxy that exposes an OpenAI-compatible API for subnet inference.
Users point any OpenAI client at `localhost:8080` and make chat completion requests; the proxy handles all subnet protocol complexity internally.

## Configuration

All settings can be passed as flags or environment variables. Flags take precedence over env vars.

| Flag | Env var | Required | Default | Description |
|------|---------|----------|---------|-------------|
| `--private-key` | `SUBNET_PRIVATE_KEY` | yes | - | Hex-encoded secp256k1 private key |
| `--escrow-id` | `SUBNET_ESCROW_ID` | yes | - | On-chain escrow ID |
| `--chain-rest` | `SUBNET_CHAIN_REST` | no | `http://localhost:1317` | Chain REST API URL |
| `--model` | `SUBNET_MODEL` | no | `Qwen/Qwen2.5-7B-Instruct` | Default model (used when request omits `model`) |
| `--port` | `SUBNET_PORT` | no | `8080` | Listen port |
| `--storage-path` | `SUBNET_STORAGE_PATH` | no | `~/.cache/gonka/subnet-<escrow-id>.db` | SQLite path for crash recovery |

## Quick start

```bash
subnetctl \
  --private-key "deadbeef..." \
  --escrow-id 42 \
  --chain-rest "http://localhost:1317"

# In another terminal:
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"Qwen/Qwen2.5-7B-Instruct","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}'
```

Or using environment variables:

```bash
export SUBNET_PRIVATE_KEY="deadbeef..."
export SUBNET_ESCROW_ID="42"
export SUBNET_CHAIN_REST="http://localhost:1317"

subnetctl
```

## Endpoints

### POST /v1/chat/completions

Standard OpenAI chat completion format. The full request body is forwarded as the inference prompt.

Request fields used by the proxy:
- `model` -- passed to InferenceParams (falls back to `SUBNET_MODEL`)
- `max_tokens` -- passed to InferenceParams (default 2048)
- `stream` -- if true, response is SSE; if false, response is a single JSON object

Returns 429 if another inference is already in flight.

### POST /v1/finalize

Triggers subnet finalization and returns settlement JSON.

No request body needed. Response is the settlement payload ready for `inferenced tx inference settle-subnet-escrow`.

### GET /v1/status

Returns current session state.

```json
{"escrow_id":"42","nonce":15,"phase":"active","balance":5000000000}
```

Phase values: `active`, `finalizing`, `settlement`.

## OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="unused")
response = client.chat.completions.create(
    model="Qwen/Qwen2.5-7B-Instruct",
    messages=[{"role": "user", "content": "Hello"}],
    max_tokens=100,
)
print(response.choices[0].message.content)
```

The `api_key` is required by the SDK but ignored by the proxy.

## Finalization and settlement

After all inferences are done:

1. POST to `/v1/finalize` -- the proxy runs the multi-phase finalization protocol, collects host signatures, and returns settlement JSON.
2. Submit the settlement on-chain: `inferenced tx inference settle-subnet-escrow settlement.json --from <user>`

The proxy holds the session open until finalization. Once finalized, the session cannot accept new inferences.

## Non-streaming vs streaming

Non-streaming (`"stream": false` or omitted): the proxy buffers all SSE chunks from the ML node and returns the final assembled JSON response.

Streaming (`"stream": true`): the proxy relays SSE `data:` lines in real time. The stream ends with `data: [DONE]`. Subnet protocol events (receipts, metadata) are filtered out -- only inference data reaches the client.
