## Multi-Genesis Setup Plan

This document defines the high-level design and operational plan to run a multi-genesis network with multiple genesis validators using a single script controlled by `GENESIS_RUN_STAGE`. It is optimized to work both for local multi-project Docker Compose setups and for production across different machines.

### Objectives
- **Enable multi-genesis bootstrapping** across N validators with manual artifact exchange between stages.
- **Use one script** (`inference-chain/scripts/init-docker-genesis.sh`) with `GENESIS_RUN_STAGE` gating behavior.
- **Mirror production** in local testing by running separate compose projects per node and connecting over the host network.

### Components
- Script: `inference-chain/scripts/init-docker-genesis.sh`
- Per-node compose projects (one per validator, each with its own `tmkms` and `node`)
- Manual artifact movement between stages (using helper scripts or operator actions)

## Environment Contract
Define these environment variables per node. Defaults should remain as in the current script unless specified.

- Identity/role
  - `GENESIS_RUN_STAGE`: `keygen | intermediate_genesis | gentx | start | restart`
  - `GENESIS_ROLE`: `coordinator | validator` (optional if using index)
  - `GENESIS_INDEX`: integer; `0` designates coordinator
  - `KEY_NAME`: key alias; use as node moniker as well

- Paths
  - `NODE_HOME`: e.g., `/root/.inference` (ephemeral until final start)
  - `KEYRING_HOME`: e.g., `/root/.keyring` (persistent)
  - `TMKMS_HOME`: e.g., `/root/.tmkms` (persistent)
  - `ARTIFACTS_DIR`: e.g., `/root/artifacts` (optional for local sharing; manual moves are supported)

- Chain/config
  - `CHAIN_ID`, `COIN_DENOM` (keep current defaults)
  - `KEYRING_BACKEND` (use `test` locally; production may use `file`)

- Networking and ports
  - `P2P_BASE` (default `26656`), `RPC_BASE` (default `26657`), `TMKMS_BASE` (default `26658`)
  - `PORT_STRIDE` default `10`
  - Derived per-node: `P2P_PORT = P2P_BASE + PORT_STRIDE * GENESIS_INDEX`, similarly for `RPC_PORT`, `TMKMS_PORT`
  - `P2P_EXTERNAL_ADDRESS`: local `tcp://host.docker.internal:${P2P_PORT}`; prod use public IP/DNS
  - `PERSISTENT_PEERS` or `PERSISTENT_PEERS_FILE` (manual artifact)

- Artifacts (manual transfer between stages)
  - `INTERMEDIATE_GENESIS_FILE`
  - `FINAL_GENESIS_FILE`
  - `PERSISTENT_PEERS_FILE`

## Directory Layout and Artifact Naming
- `validators/<name>/account_addr.txt`
- `validators/<name>/val_pubkey.json`
- `validators/<name>/node_id.txt`
- `addresses/<name>.txt` (same as account address; for coordinator input)
- `gentxs/<name>.json`
- `genesis/intermediate_genesis.json`
- `genesis/final_genesis.json`
- `peers/persistent_peers.txt`

## GENESIS_RUN_STAGE Behaviors

### GENESIS_RUN_STAGE=keygen (all validators, including coordinator)
- Inputs: `KEY_NAME`, `GENESIS_INDEX`, derived ports
- Actions:
  - Create account key in `KEYRING_HOME` (always use `--keyring-dir "$KEYRING_HOME"`).
  - Start `tmkms` and `node` with `priv_validator_laddr` to obtain validator pubkey and `node_id`.
  - Emit artifacts under `validators/<name>/`:
    - `account_addr.txt`
    - `val_pubkey.json`
    - `node_id.txt`
  - Wipe `NODE_HOME` afterwards; keep `TMKMS_HOME` and `KEYRING_HOME`.
- Outputs: The three artifacts per validator.

### GENESIS_RUN_STAGE=intermediate_genesis (coordinator only)
- Inputs: `addresses/*.txt` (collected manually from validators)
- Actions:
  - Fresh `NODE_HOME`; run `init` and add all addresses using current denom/amounts.
  - Produce `genesis/intermediate_genesis.json`.
  - Wipe `NODE_HOME` afterwards.
- Outputs: `intermediate_genesis.json`.

### GENESIS_RUN_STAGE=gentx (all validators)
- Inputs:
  - `genesis/intermediate_genesis.json` (placed manually into this node)
  - Own `validators/<name>/val_pubkey.json`
  - Account key in `KEYRING_HOME`
- Actions:
  - Fresh `NODE_HOME`; install intermediate genesis.
  - Run `inferenced genesis gentx` using pubkey support (keep current params in script).
  - Emit `gentxs/<name>.json`.
  - Wipe `NODE_HOME` (recommended) after writing gentx.
- Outputs: `gentx` for each validator.

### GENESIS_RUN_STAGE=start
- Coordinator:
  - Inputs: all `gentxs/*.json` (collected manually), `intermediate_genesis.json`.
  - Actions:
    - Fresh `NODE_HOME`; install intermediate genesis; run `collect-gentxs` to produce final genesis.
    - Build `peers/persistent_peers.txt` (see algorithm below).
    - Start cosmovisor/node.
  - Outputs: `genesis/final_genesis.json`, `peers/persistent_peers.txt`.

- Validators:
  - Inputs: `genesis/final_genesis.json`, `peers/persistent_peers.txt` (moved manually).
  - Actions: install final genesis; set `p2p.persistent_peers` from file or env; start cosmovisor/node.

### GENESIS_RUN_STAGE=restart
- Inputs: existing `NODE_HOME` (from a running network)
- Actions: start cosmovisor only; no initialization.

## Persistent Peers Algorithm (Coordinator)
- Read `validators/*/node_id.txt` and each node’s `GENESIS_INDEX` (or pass a manifest).
- Compute per-node `P2P_PORT = P2P_BASE + PORT_STRIDE * GENESIS_INDEX`.
- Build addresses:
  - Local: `node_id@host.docker.internal:P2P_PORT`
  - Production: `node_id@<public-host-or-ip>:<p2p-port>` (reflect each node’s `P2P_EXTERNAL_ADDRESS`)
- Output a comma-separated list to `peers/persistent_peers.txt`.
- Validators consume this file; the script writes it into `config.toml` as `p2p.persistent_peers`.

## Port Scheme
- For index `i`: `PORT = BASE + (PORT_STRIDE * i)`
- Apply to `P2P_BASE`, `RPC_BASE`, `TMKMS_BASE` with `PORT_STRIDE = 10` by default.

## Data Retention and Wipe Policy
- Keep forever: `TMKMS_HOME`, `KEYRING_HOME`, and artifacts.
- Wipe between stages: `NODE_HOME` after `keygen`, `intermediate_genesis`, and `gentx`.
- Keep once final network starts: `NODE_HOME` from `start` onward.

## Local Multi-Project Compose Topology
- Run one compose project per node (mirrors production separation).
- Publish P2P/RPC/TMKMS ports on the host. Use `host.docker.internal` inside containers to reach other nodes.
- Set `P2P_EXTERNAL_ADDRESS=tcp://host.docker.internal:${P2P_PORT}` locally. In production, use public IP/DNS.
- Keep `seeds = ""` for genesis nodes; rely on `persistent_peers`.
- Allow duplicate IPs locally via `CONFIG_p2p__allow_duplicate_ip=true`.

## Local Runbook (Manual Artifact Movement)
1) All nodes: `GENESIS_RUN_STAGE=keygen`
   - Collect each node’s `account_addr.txt`, `val_pubkey.json`, `node_id.txt`.
2) Coordinator: `GENESIS_RUN_STAGE=intermediate_genesis`
   - Produce `genesis/intermediate_genesis.json`.
3) Distribute `intermediate_genesis.json` to all nodes.
4) All nodes: `GENESIS_RUN_STAGE=gentx`
   - Produce `gentxs/<name>.json`.
5) Collect all `gentxs/*.json` on coordinator.
6) Coordinator: `GENESIS_RUN_STAGE=start`
   - Produce `genesis/final_genesis.json` and `peers/persistent_peers.txt`; start node.
7) Distribute `final_genesis.json` and `persistent_peers.txt` to validators.
8) Validators: `GENESIS_RUN_STAGE=start`
   - Start nodes.
9) Any node restarts: `GENESIS_RUN_STAGE=restart`.

## Production Notes
- Same stages and artifacts as local. Do not share Docker networks across machines.
- Set `P2P_EXTERNAL_ADDRESS` to public IP/DNS. Ensure ports are reachable.
- Only move public artifacts (addresses, pubkeys, gentxs, genesis, peers). Never move `KEYRING_HOME` or `TMKMS_HOME`.

## Implementation Tasks
1) Script scaffolding
   - Add `GENESIS_RUN_STAGE`, `GENESIS_INDEX|GENESIS_ROLE`, path/port envs, and derived port calculation.
   - Ensure all key commands use `--keyring-dir "$KEYRING_HOME"`.

2) Keygen mode
   - Start TMKMS + node with `priv_validator_laddr`.
   - Capture and write `account_addr.txt`, `val_pubkey.json`, `node_id.txt`.
   - Wipe `NODE_HOME`.

3) Intermediate genesis mode (coordinator)
   - Fresh `NODE_HOME`; `init`; add accounts from `addresses/`.
   - Write `genesis/intermediate_genesis.json`; wipe `NODE_HOME`.

4) Gentx mode (validators)
   - Install `intermediate_genesis.json`.
   - Use saved `val_pubkey.json`; run `gentx` with current params.
   - Write `gentxs/<name>.json`; wipe `NODE_HOME`.

5) Start mode
   - Coordinator: collect gentxs; write final genesis; build peers; start.
   - Validators: install final genesis; set peers; start.

6) Restart mode
   - Start cosmovisor only.

7) Safety and idempotency
   - Add `FORCE` handling, sanity checks (exactly one coordinator for collect, presence of required artifacts, consistent `CHAIN_ID`).
   - Clear logging of Inputs/Outputs per stage.

8) Compose guidance
   - Document per-project env to set `GENESIS_INDEX`, `P2P_EXTERNAL_ADDRESS`, and bind-mount per-node homes.

## Acceptance Criteria
- Local: 3+ nodes reach consensus and produce blocks; validators show connected peers as per `persistent_peers`.
- No private keys leave their hosts; only public artifacts and genesis files are transferred.
- Re-runnable stages with `FORCE` and wipe policy respected.
- Ports derived via `BASE + 10 * index`.

## Security Notes
- Treat `KEYRING_HOME` and `TMKMS_HOME` as sensitive; never distribute.
- Validate all imported artifacts with checksums (optional `sha256sum.txt` per directory).


