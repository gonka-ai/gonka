# `deploy/join`

Compose stack for joining the gonka network.

## Setup A — validator only (default)

The validator stack lives in `docker-compose.yml`. Optional companions
`docker-compose.mlnode.yml` and `docker-compose.postgres.yml` combine in
any order.

```bash
cp config.env.template config.env
# edit config.env (KEY_NAME, ACCOUNT_PUBKEY, P2P_EXTERNAL_ADDRESS, …)
source config.env
docker compose up -d
```

That is the entire deploy. Sentry is optional and never required.

---

## Setup B — sentry mesh (optional)

A sentry mesh shields the validator's identity from the public P2P
network. Each sentry is a public relay that proxies tendermint traffic
to the validator; the validator itself only peers with its sentries
(no public P2P).

This setup is **add-on** — you can layer it onto a validator that has
been running in production for any length of time, and roll it back
without losing state.

### Topology

```
                ┌───────── public network ─────────┐
                │                                  │
                ▼                                  ▼
         ┌──────────┐                        ┌──────────┐
         │ sentry   │ ◀─persistent_peers─▶  │ sentry-2 │
         │ host A   │                        │ host B   │
         └────┬─────┘                        └────┬─────┘
              │           full mesh              │
              ▼                                  ▼
              └────── persistent_peers ─────────┐
                                                ▼
                                       ┌──────────────┐
                                       │  validator   │
                                       │  (no public  │
                                       │   P2P)       │
                                       └──────────────┘
```

- **Validator** lists every sentry in `persistent_peers` +
  `unconditional_peer_ids`. Validator dials sentries (outbound only —
  it has no public P2P port after isolation).
- **Each sentry** lists *other sentries* in `persistent_peers`
  (sentry-to-sentry mesh). The validator is **not** in any sentry's
  `persistent_peers` because after isolation its port is closed —
  dialing it would loop-fail. Instead, the validator is in every
  sentry's `unconditional_peer_ids`, so the inbound connection
  initiated by the validator is always accepted.
- **`private_peer_ids=<validator>`** on every sentry — critical so the
  sentry never advertises the validator's node-id over PEX gossip.
- **api / proxy** on the validator host stay on the local node.
  Isolation is P2P-only — chain-RPC/API/gRPC continue to be served
  by the validator's RPC.

### Files

| File | Role |
|---|---|
| `docker-compose.sentry.yml` | Standalone sentry compose (`name: sentry`). One file, anchors + `--profile multi` for sentry-2/3 on the same host. |
| `docker-compose.sentry-isolate.yml` | Overlay on validator stack. P2P-only. |
| `config.env.sentry.template` | Sentry-side env template. Lives next to validator's `config.env.template` but is meant for sentry hosts. |

### Phase 1 — bring up sentry(es)

Sentry runs as its own compose project, on its own network.
Validator stack is **not touched** — no overlay, no recreate.

The compose file is identical for same-host or remote-host deployment;
only the public addresses differ.

On each sentry host (could be the validator host, or a separate server):

```bash
# Copy the file to the sentry host (only this file is needed; the
# validator's docker-compose.yml is irrelevant for sentry).
cp docker-compose.sentry.yml /destination/host/.../

# Sentry-side env
cp config.env.sentry.template config.env.sentry
# edit config.env.sentry: CHAIN_ID, seeds, P2P_EXTERNAL_ADDRESS,
# SENTRY_KEY_NAME, SENTRY_P2P_PORT — leave VALIDATOR_ID /
# SENTRY_PERSISTENT_PEERS empty for now.
source config.env.sentry

# Bring up sentry
docker compose -f docker-compose.sentry.yml up -d
```

For multiple sentries on the same host:

```bash
docker compose -f docker-compose.sentry.yml --profile multi up -d
```

Sentry generates its own keys, downloads genesis from `SEED_NODE_RPC_URL`,
state-syncs from public RPCs, joins the public mesh as a regular relay.
Validator keeps running unchanged.

If you decide you don't want sentry: `docker compose -f
docker-compose.sentry.yml down` (and optionally remove
`.inference-sentry*` volumes). Validator never moved.

### Phase 2 — collect node-ids and stitch the mesh

When every sentry is following chain head:

```bash
# On the validator host
docker exec node    inferenced tendermint show-node-id

# On each sentry host
docker exec sentry  inferenced tendermint show-node-id
```

Collect into a small table — `<node-id>@<public-host>:<p2p-port>` per
node. Then:

**Validator's `config.env`** (same `deploy/join/`):

```bash
export NODE_PERSISTENT_PEERS=<sentry1-id>@<hostA>:5001,<sentry2-id>@<hostB>:5001
export NODE_UNCONDITIONAL_PEER_IDS=<sentry1-id>,<sentry2-id>
export NODE_MAX_INBOUND=2
export NODE_MAX_OUTBOUND=2
```

**Each sentry's `config.env.sentry`** (`persistent_peers` = other
sentries only, validator only in `unconditional_peer_ids`):

```bash
# On host running sentry-1:
export VALIDATOR_ID=<vid>
export SENTRY_PERSISTENT_PEERS=<sentry2-id>@<hostB>:5001
export SENTRY_UNCONDITIONAL_PEER_IDS=<vid>,<sentry2-id>

# On host running sentry-2:
export VALIDATOR_ID=<vid>
export SENTRY_PERSISTENT_PEERS=<sentry1-id>@<hostA>:5001
export SENTRY_UNCONDITIONAL_PEER_IDS=<vid>,<sentry1-id>
```

(For sentry-2/sentry-3 running together with sentry-1 on a single host
via `--profile multi`, the same host's `config.env.sentry` populates
`SENTRY_*`, `SENTRY2_*`, `SENTRY3_*` — see the template.)

Re-up each sentry to pick up the new mesh:

```bash
source config.env.sentry
docker compose -f docker-compose.sentry.yml up -d
# (or with --profile multi if applicable)
```

Sentries restart, validator unchanged. Each sentry now has
`private_peer_ids=<validator>` so it never leaks the validator's
node-id over PEX, and full-mesh `persistent_peers` so blocks/txs flow
between members reliably.

### Phase 3 — isolate validator (P2P only)

Only do this once **all** sentries are mesh-ed and synced. This step
recreates the validator's `node` container — expect a brief
P2P-disconnect (api/proxy keep serving uninterrupted).

```bash
source config.env
docker compose -f docker-compose.yml \
               -f docker-compose.sentry-isolate.yml up -d
```

After this:

- Validator's public `5000:26656` is closed (`ports: !reset []`).
- Validator only peers with the listed sentries
  (`pex=false`, `persistent_peers`/`unconditional_peer_ids`).
- Each sentry already shields the validator's node-id via
  `private_peer_ids` (set in phase 2).
- api / proxy keep serving public RPC from the local node.

### Adding sentries later

Bring up a new sentry on any host (same flow as phase 1), then update
**every existing** sentry's `config.env.sentry` and the validator's
`config.env` to include the new node in the mesh, and re-up. You can
mix same-host (`--profile multi`) and separate-host sentries freely —
the configuration scales the same way.

### Verifying isolation

After phase 3:

```bash
# Validator P2P closed:
nc -vz <validator-host> 5000        # connection refused / closed

# Sentry P2P open:
nc -vz <sentry-host>    5001        # open

# Validator's persistent_peers point at sentries, pex disabled:
docker exec node grep -E "^pex|^persistent_peers" \
  /root/.inference/config/config.toml
# pex = false
# persistent_peers = "...@host:5001,..."
```

api/proxy continue serving public RPC from the local node — there is
no chain-RPC dependency on sentry. Stopping every sentry will only
affect block production; chain-RPC stays up.

### Rolling back

**Phase 1 (sentry up, no isolation):**
```bash
docker compose -f docker-compose.sentry.yml down
sudo rm -rf .inference-sentry*       # optional: clear sentry state
```
Validator untouched.

**Phase 3 (validator isolated):**
```bash
docker compose -f docker-compose.yml up -d        # drop the isolate overlay
```
Validator's public port comes back, persistent_peers cleared. Sentries
stay as additional public nodes.

### Tuning app.toml on the validator (pruning, fastnode)

`init-docker.sh` exposes an `APP_*` env prefix that mirrors `CONFIG_*`
but writes into `app.toml`. Substitution rules: `__` → `.`, `_` → `-`.

The isolate overlay sets validator-side defaults so it prunes
aggressively and keeps fastnode on. Override in `config.env`:

```bash
# export NODE_PRUNING=custom
# export NODE_PRUNING_KEEP_RECENT=100
# export NODE_PRUNING_INTERVAL=1500
# export NODE_MIN_RETAIN_BLOCKS=0
# export NODE_IAVL_DISABLE_FASTNODE=false
```

`APP_*` support requires `init-docker.sh` patched with the APP_*
override commit — stock images without the patch silently ignore them.

---

## Requirements

- Docker Compose `>= 2.20` for the `ports: !reset []` directive used
  in `docker-compose.sentry-isolate.yml`.
- Compose `>= 2.3.3` for the top-level `name:` directive in
  `docker-compose.sentry.yml`.

## Image pinning

Image tags are hardcoded in compose files:

- `docker-compose.yml` `node` → `ghcr.io/product-science/inferenced:0.2.11`
- `docker-compose.sentry.yml` `x-sentry-base` → same default

Edit those lines to switch versions.
