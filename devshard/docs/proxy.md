# devshardctl

## Build

```
go build -o devshardctl ./cmd/devshardctl/
```

Local HTTP proxy that exposes an OpenAI-compatible API for devshard inference.
Users point any OpenAI client at `localhost:8080` and make chat completion requests; the proxy handles all devshard protocol complexity internally.

## Configuration

All settings can be passed as flags or environment variables. Flags take precedence over env vars.

| Flag | Env var | Required | Default | Description |
|------|---------|----------|---------|-------------|
| `--private-key` | `DEVSHARD_PRIVATE_KEY` | yes | - | Hex-encoded secp256k1 private key |
| `--escrow-id` | `DEVSHARD_ESCROW_ID` | yes | - | On-chain escrow ID |
| `--chain-rest` | `DEVSHARD_CHAIN_REST` | no | `http://localhost:1317` | Chain REST API URL |
| `--model` | `DEVSHARD_MODEL` | no | `Qwen/Qwen3-4B-Instruct-2507` | Default model (used when request omits `model`) |
| `--port` | `DEVSHARD_PORT` | no | `8080` | Listen port |
| `--storage-path` | `DEVSHARD_STORAGE_PATH` | no | `~/.cache/gonka/devshard-<escrow-id>.db` | SQLite path for crash recovery |

## Quick start

## Create Escrow

```
./inferenced tx inference \
  create-devshard-escrow 5000000000 \
  --from dev1 \
  --keyring-backend file \
  --home ~/testnet-2 \
  --chain-id gonka-testnet-2 \
  --node http://89.169.110.61:8000/chain-rpc/ \
  --gas auto \
  --gas-adjustment 1.5 \
  --fees 500000ngonka -y
```

## Start proxy

```bash
devshardctl \
  --private-key "deadbeef..." \
  --escrow-id 42 \
  --chain-rest http://89.169.110.61:8000/chain-api/ \
  --model Qwen/Qwen3-4B-Instruct-2507

# In another terminal:
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"Qwen/Qwen3-4B-Instruct-2507","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}'
```

## Finalize Escrow 

```
curl -X 'POST' http://localhost:8080/v1/finalize > ./settle.json
```

## Settle Escrow

```
./inferenced tx inference \
  settle-devshard-escrow \
  settle.json \
  --from dev1 \
  --keyring-backend file \
  --home ~/testnet-2 \
  --chain-id gonka-testnet-2 \
  --node http://89.169.110.61:8000/chain-rpc/ \
  --gas auto \
  --gas-adjustment 1.5 \
  --fees 500000ngonka -y
```

## Endpoints

### POST /v1/chat/completions

Standard OpenAI chat completion format. The full request body is forwarded as the inference prompt.

Request fields used by the proxy:
- `model` -- passed to InferenceParams (falls back to `DEVSHARD_MODEL`)
- `max_tokens` -- passed to InferenceParams (default 2048)
- `stream` -- if true, response is SSE; if false, response is a single JSON object


### POST /v1/finalize

Triggers devshard finalization and returns settlement JSON.

### GET /v1/status

## Finalization and settlement

After all inferences are done:

1. POST to `/v1/finalize` -- the proxy runs the multi-phase finalization protocol, collects host signatures, and returns settlement JSON.
2. Submit the settlement on-chain: `inferenced tx inference settle-devshard-escrow settlement.json --from <user>`

The proxy holds the session open until finalization. Once finalized, the session cannot accept new inferences.

