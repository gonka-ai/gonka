# `deploy/join`

Compose stack for joining the gonka network. The base `docker-compose.yml`
is a single-node setup; optional overlays change the topology.

## Setup A — single node (default)

Standard validator with public P2P. Run as-is:

```bash
docker compose up -d
```

This is the original layout — unchanged. Optional overlays
`docker-compose.mlnode.yml` and `docker-compose.postgres.yml` can be
combined with the base in any order.

## Setup B — validator behind sentry node(s)

Setup B places the validator in a private P2P network behind one or
more public-facing sentry relays. Recommended for production
validators that should not expose their P2P address publicly.

What changes vs Setup A:

- Validator P2P (host port 5000) is no longer published.
- Validator only peers with the configured sentry/sentries
  (`pex=false`, fixed `persistent_peers`).
- Validator's `seeds` and state-sync RPC URLs point to sentry, not to
  public seed nodes. This prevents the validator's IP from being
  exposed during init or state-sync.
- Public chain-RPC/API/gRPC arriving at the proxy is forwarded to
  sentry instead of node.

Three overlays are provided: `docker-compose.sentry.yml`,
`docker-compose.sentry-2.yml`, `docker-compose.sentry-3.yml`. They are
additive — apply only as many as you need.

### Bootstrap workflow (two-phase)

The validator-sentry topology has a chicken-and-egg dependency: the
validator's `persistent_peers` needs each sentry's node id, and each
sentry's `private_peer_ids` (which prevents PEX leakage of the
validator) needs the validator's node id. The fix is two `up -d`
rounds — first to generate node-ids, second to apply the final P2P
configuration.

#### One sentry

```bash
# 1. First up: brings sentry + validator + tmkms up. Both nodes init,
#    generate keys, advertise their node-ids. P2P configuration is
#    intentionally incomplete on this round (peer ids are empty).
source config.env
docker compose -f docker-compose.yml -f docker-compose.sentry.yml up -d

# 2. Read the generated node-ids.
docker exec node    inferenced tendermint show-node-id   # -> VALIDATOR_ID
docker exec sentry  inferenced tendermint show-node-id   # -> SENTRY_NODE_ID

# 3. Append to config.env:
#      export VALIDATOR_ID=<id-from-node>
#      export SENTRY_NODE_ID=<id-from-sentry>
#      export SENTRY_PERSISTENT_PEERS=${VALIDATOR_ID}@node:26656
#      export NODE_PERSISTENT_PEERS=${SENTRY_NODE_ID}@sentry:26656
#      export NODE_UNCONDITIONAL_PEER_IDS=${SENTRY_NODE_ID}

# 4. Second up: Compose recreates containers with the new env. On
#    container start init-docker.sh writes the final P2P settings into
#    config.toml.
source config.env
docker compose -f docker-compose.yml -f docker-compose.sentry.yml up -d
```

#### Two/three sentries

Same two-phase flow with the additional overlays:

```bash
# Phase 1
docker compose -f docker-compose.yml \
               -f docker-compose.sentry.yml \
               -f docker-compose.sentry-2.yml \
               -f docker-compose.sentry-3.yml up -d

# Read all four node-ids
docker exec node     inferenced tendermint show-node-id
docker exec sentry   inferenced tendermint show-node-id
docker exec sentry-2 inferenced tendermint show-node-id
docker exec sentry-3 inferenced tendermint show-node-id

# Fill config.env (example for three sentries)
#   export VALIDATOR_ID=<id>
#   export SENTRY_NODE_ID=<id>
#   export SENTRY2_NODE_ID=<id>
#   export SENTRY3_NODE_ID=<id>
#   export SENTRY_PERSISTENT_PEERS=${VALIDATOR_ID}@node:26656
#   export NODE_PERSISTENT_PEERS=${SENTRY_NODE_ID}@sentry:26656,${SENTRY2_NODE_ID}@sentry-2:26656,${SENTRY3_NODE_ID}@sentry-3:26656
#   export NODE_UNCONDITIONAL_PEER_IDS=${SENTRY_NODE_ID},${SENTRY2_NODE_ID},${SENTRY3_NODE_ID}

# Phase 2
source config.env
docker compose -f docker-compose.yml \
               -f docker-compose.sentry.yml \
               -f docker-compose.sentry-2.yml \
               -f docker-compose.sentry-3.yml up -d
```

`config.env.template` lists every sentry-related variable with
commented examples.

### What each P2P setting does

After phase 2, the validator and sentry config.toml files should
contain the following (this is the security envelope of the topology):

- **Validator:** `pex=false`, `persistent_peers=<all sentries>`,
  `unconditional_peer_ids=<all sentry ids>`, `addr_book_strict=false`.
  Validator only dials/accepts its sentries.
- **Sentry:** `pex=true` (relay role), `persistent_peers=<validator>`,
  `private_peer_ids=<validator id>`, `unconditional_peer_ids=<validator id>`,
  `addr_book_strict=false`. **`private_peer_ids` is the critical one —
  without it the sentry would advertise the validator's node-id to
  the public network through PEX gossip.**

### Tuning app.toml (pruning, fastnode, snapshots)

`init-docker.sh` exposes an `APP_*` env prefix that mirrors the
existing `CONFIG_*` prefix but writes into `app.toml`. Substitution
rules:

  `__`  →  `.`   (nested keys: `APP_state__sync__snapshot_interval`)
  `_`   →  `-`   (kebab keys:   `APP_pruning_keep_recent`)

The sentry overlay sets the validator-side defaults so it prunes
aggressively and keeps fastnode on (fastnode delivers ~99% lag
reduction during pruning). Override in config.env if needed:

```bash
# Validator app.toml defaults wired in docker-compose.sentry.yml:
#   APP_pruning=custom
#   APP_pruning_keep_recent=100
#   APP_pruning_interval=1500
#   APP_min_retain_blocks=0
#   APP_iavl_disable_fastnode=false
#
# To deviate, set NODE_PRUNING / NODE_PRUNING_INTERVAL / etc in config.env.
```

The same APP_* mechanism works for any service that runs init-docker.sh
— set the env var on a service and the value lands in its app.toml.

### Adding a fourth+ sentry

Copy `docker-compose.sentry-3.yml` to `docker-compose.sentry-4.yml`,
replace `3 → 4` and `5003 → 5004` everywhere, add the corresponding
`SENTRY4_*` block to `config.env`, and append
`${SENTRY4_NODE_ID}@sentry-4:26656` to `NODE_PERSISTENT_PEERS`.

### Verifying isolation

After Setup B is up, the validator should be unreachable from outside
on its P2P port and its `seeds` should point only at sentry:

```bash
# Validator P2P closed:
nc -vz <host> 5000   # connection refused / closed

# Sentry P2P open:
nc -vz <host> 5001   # open

# Validator seeds in config.toml point at sentry, not public seeds:
docker exec node grep -E "^seeds|^persistent_peers" /root/.inference/config/config.toml
# seeds = "...@sentry:26656"
# persistent_peers = "...@sentry:26656"
```

### Public RPC routing through sentry

Stopping sentry should make the public chain-RPC fail — this is the
quickest way to confirm the proxy is forwarding to sentry rather than
to the validator:

```bash
docker stop sentry
curl -i http://<host>:8000/chain-rpc/status
# expected: 502 Bad Gateway / connection refused
docker start sentry
```

## Requirements

- Docker Compose `>= 2.20` for the `ports: !reset []` directive used
  in `docker-compose.sentry.yml` to detach the validator from its
  public P2P port.

## Image pinning

By default both `node` and the sentry services use
`ghcr.io/product-science/inferenced:0.2.11`. To pin a different
version, export `INFERENCED_IMAGE` in `config.env`:

```bash
export INFERENCED_IMAGE=ghcr.io/product-science/inferenced:<tag>
```
