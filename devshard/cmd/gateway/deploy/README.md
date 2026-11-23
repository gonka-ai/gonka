# Running the devshard gateway

This directory holds the self-hosted Docker Compose stack for the gateway in `devshard/cmd/gateway`. Follow the steps below to bring up one gateway next to a node; the settings behind them are described in [`docs/configuration.md`](../docs/configuration.md).

| File | What it is |
| --- | --- |
| `docker-compose.devshard-gateway.yml` | one service, joined to the node's network |
| `config.devshard-gateway.env.template` | the variables this stack sets, with the defaults it ships; the full list the gateway reads is [`env/env.go`](../env/env.go) |

## Run it

1. Bring the node's own stack up first. The compose file joins the external network `join_default`, which the node's stack creates; without it the gateway refuses to start with `network join_default declared as external, but could not be found`.

2. Copy the template and fill it in ("What you must fill in"):

   ```bash
   cp config.devshard-gateway.env.template config.devshard-gateway.env
   $EDITOR config.devshard-gateway.env
   ```

3. Choose the image and start the container. `GATEWAY_IMAGE` is a full image reference with its tag; building one is in [`docs/runbook.md`](../docs/runbook.md), "Release and rollback":

   ```bash
   export GATEWAY_IMAGE=<registry>/<image>:<tag>
   docker compose -f docker-compose.devshard-gateway.yml up -d
   ```

4. Check that it answers. The gateway listens on `127.0.0.1:18080` (`GATEWAY_HOST_PORT`), bound to loopback so the chat API is reachable from the host but not from the network:

   ```bash
   curl -s localhost:18080/v1/status
   ```

## What you must fill in

These variables have no working default:

| Variable | What it is |
| --- | --- |
| `DEVSHARD_CHAIN_RPC`, `DEVSHARD_CHAIN_GRPC`, `DEVSHARD_CHAIN_ID`, `DEVSHARD_PUBLIC_API` | the chain the gateway reads and broadcasts to. A wrong `DEVSHARD_CHAIN_ID` invalidates every transaction it signs. The gateway runs with `DEVSHARD_CHAIN_GRPC` unreachable as long as `DEVSHARD_CHAIN_RPC` points at a live CometBFT RPC |
| `DEVSHARD_ADMIN_API_KEY` | the admin key. Empty disables the admin routes entirely, including creating escrows, setting `access` and creating client keys |

Client keys are not an environment setting, bar the legacy variable below: after the first start, create each with `POST /v1/admin/api-keys` for the models whose `access` under `models` (or the network `access`) is `api_key`. A fresh gateway serves no client until the operator sets `access` for a model under `models` and, for an `api_key` model, creates a key: a model with no `access` of its own answers only the admin key ([`docs/operations.md`](../docs/operations.md), "Who may call what", "Client API keys").

## Legacy inputs

- Legacy `DEVSHARD_API_KEYS` (deprecated, removal planned for v7 or v8): the variable is still read, comma-separated and each key at least 16 characters, and its keys are active beside the database keys; create them through `POST /v1/admin/api-keys` instead. The list and a read by id show them with `source` `env`, no value and no `created_at`, and they cannot be revoked or created again through the API (409): to remove one, move its clients to a new key, then unset the variable.
- Legacy `model_limits` (deprecated, removal planned for v7 or v8): the settings key is still accepted and read as `models.<id>` with the same field names; set `models.<id>` instead, which wins field by field, and a boot or a write holding the key logs a warning.
- Legacy `model_access` (deprecated, removal planned for v7 or v8): the settings key is still read as `models.<id>.access`; set `models.<id>.access` instead, which wins when both name the model, and a boot or a write holding the key logs a warning.

## Signing keys are named, never pasted

`DEVSHARDS_JSON` seeds the escrows to serve at startup, and each entry names the variable that holds the key rather than the key:

```json
[{"id": "63362", "private_key_env": "DEVSHARD_PRIVATE_KEY", "model": "deepseek-ai/DeepSeek-V4-Flash-0731"}]
```

The gateway then reads `DEVSHARD_PRIVATE_KEY` from its environment. The admin API takes a variable name for the same reason: a key pasted into a request body would reach the logs, the shell history and the audit trail. Leave `DEVSHARDS_JSON` empty to start with no escrows and add them through the admin API.

## Storage

`DEVSHARD_STORAGE_HOST_DIR` (default `.devshard-gateway`, next to the compose file) is mounted at `DEVSHARD_STORAGE_DIR` in the container. It holds the escrow database, the per-escrow session state and the nonce ledger. Losing it loses the gateway's memory of the escrows it serves, so point it somewhere durable and back it up ([`docs/runbook.md`](../docs/runbook.md), "Backup and restore").

## What the healthcheck means

The container's healthcheck probes `/healthz`, which answers 200 without checking anything else and stays up through the kill switch. `docker compose ps` reporting `healthy` therefore means the process is up and answering, not that it is serving chat.

| Route | Why the healthcheck does not use it |
| --- | --- |
| `/v1/status` | every probe would build the status of every escrow. It also does not say whether chat is served: its `requests_blocked` reports the chain's admission gate, while a disabled gateway answers 503, or 308 with a redirect set, on the chat routes |
| `/metrics` | every probe would be a full scrape |

## Settings to decide before the first run

Most limits in the template are boot defaults that the admin API can override at runtime without a redeploy ([`docs/configuration.md`](../docs/configuration.md), "The three layers"). Two settings decide behaviour from the first request:

| Variable | Shipped value | Effect |
| --- | --- | --- |
| `DEVSHARD_STATS_ENABLED` | `false`, which is also the built-in default | with it off there are no counters, no findings and no `accounting.db`, and no error says so |
| `DEVSHARD_POC_REQUEST_MODE` | `relaxed`, which is also the built-in default | the gateway serves through the chain phase that otherwise blocks new inferences. Set `off` where the chain's own admission must hold |
