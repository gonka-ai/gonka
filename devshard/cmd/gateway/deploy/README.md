# Running the devshard gateway

This is the self-hosted stack for the gateway in `devshard/cmd/gateway`.

| File | What it is |
| --- | --- |
| `docker-compose.devshard-gateway.yml` | one service, joined to the node's network |
| `config.devshard-gateway.env.template` | the variables this stack sets, with the defaults it ships; the full list the gateway reads is [`env/env.go`](../env/env.go) |

## Before you start

The compose file joins the external network `join_default`, which the node's own stack creates. Bring the node up first, or the gateway will refuse to start with `network join_default declared as external, but could not be found`.

## Run it

```
cp config.devshard-gateway.env.template config.devshard-gateway.env
$EDITOR config.devshard-gateway.env
docker compose -f docker-compose.devshard-gateway.yml up -d
```

The gateway answers on `127.0.0.1:18080` (`GATEWAY_HOST_PORT`), bound to loopback by design: the chat API is reachable from the host but not from the network. Check it with

```
curl -s localhost:18080/v1/status
```

The container's healthcheck deliberately probes a *different* route, `/healthz`, which the kill switch leaves up and which does no work. `/v1/status` stays up through the kill switch too, so a gateway an operator had disabled on purpose is not restarted either way, but every probe of it would build the status of every escrow. Pointed at `/metrics`, every probe would be a full scrape. So `docker compose ps` reporting `healthy` means the process is up and answering, not that it is serving chat. `/v1/status` does not answer that alone either: its `requests_blocked` reports the chain's admission gate, while a disabled gateway answers 503 (or 308, with a redirect set) on the chat routes.

## What you must fill in

Nothing has a working default for these:

- `DEVSHARD_CHAIN_RPC`, `DEVSHARD_CHAIN_GRPC`, `DEVSHARD_CHAIN_ID`, `DEVSHARD_PUBLIC_API` — the chain the gateway reads and broadcasts to. A wrong `DEVSHARD_CHAIN_ID` invalidates every transaction it signs.
- `DEVSHARD_API_KEYS` — the keys clients present, for the models the `model_access` admin override marks `api_key`. A model that override does not name answers only the admin key, so a fresh gateway serves no client until the operator sets it ([`docs/operations.md`](../docs/operations.md), "Who may call what").
- `DEVSHARD_ADMIN_API_KEY` — empty disables the admin routes entirely, including creating escrows and setting `model_access`, so every model stays admin-only and the gateway serves no client. Set it.

## Signing keys are named, never pasted

`DEVSHARDS_JSON` seeds the escrows to serve at startup, and each entry names the **variable** that holds the key rather than the key:

```json
[{"id": "63362", "private_key_env": "DEVSHARD_PRIVATE_KEY", "model": "deepseek-ai/DeepSeek-V4-Flash-0731"}]
```

The gateway then reads `DEVSHARD_PRIVATE_KEY` from its environment. This is why the admin API takes a variable name too: a key pasted into a request body would reach the logs, the shell history and the audit trail. Leave `DEVSHARDS_JSON` empty to start with no escrows and add them through the admin API.

## Storage

`DEVSHARD_STORAGE_HOST_DIR` (default `.devshard-gateway`, next to the compose file) is mounted at `DEVSHARD_STORAGE_DIR` in the container. It holds the escrow database, the per-escrow session state and the nonce ledger. Losing it loses the gateway's memory of the escrows it serves, so back it up or point it somewhere durable before running anything that matters.

## Tuning without a redeploy

Most limits are also runtime overrides through the admin API, so the values here are the boot defaults rather than the last word. Which keys are live and what each one means is in [operations.md](../docs/operations.md), under "Configuration".

Two settings decide behaviour before the first run:

- `DEVSHARD_STATS_ENABLED` ships as `false` and the built-in default is also off: a config that never sets it gets no counters, no findings and no `accounting.db`, without an error.
- `DEVSHARD_POC_REQUEST_MODE` ships as `relaxed` and the built-in default is also relaxed: the gateway serves through the chain phase that otherwise blocks new inferences. It is the right setting for a gateway that must keep answering across an epoch boundary; set `off` where the chain's own admission must hold.
