# Gonka testnet — host join guide

Use this document to bring up a new host from scratch and join **gonka-testnet**: sync the chain via state sync, register as a participant, and serve inference.

## Quick links


| Resource                   | URL                                                                                                                       |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Validator dashboard (seed) | [http://89.169.110.250:8000/dashboard/gonka/validator](http://89.169.110.250:8000/dashboard/gonka/validator)              |
| Chain binary (on-chain)    | [inferenced v0.2.17-testnet-1](https://github.com/product-science/race-releases/releases/tag/release%2Fv0.2.17-testnet-1) |
| CLI for keygen (off-chain) | [inferenced v0.2.16](https://github.com/gonka-ai/gonka/releases/tag/release/v0.2.16)                                      |
| Official quickstart        | [https://gonka.ai/docs/host/quickstart/](https://gonka.ai/docs/host/quickstart/)                                          |
| Repo branch for deploy     | [main](https://github.com/gonka-ai/gonka)                                                                                 |




## Prerequisites

- Linux host with **Docker** (and NVIDIA stack if you run `mlnode`)
- Public **P2P** and **API** endpoints reachable from the internet
- Ports mapped per your `PUBLIC_URL` / `P2P_EXTERNAL_ADDRESS` (example below uses `19234` / `19233`)

Set these shell variables when querying the seed:

```bash
export NODE_RPC=http://89.169.111.79:8000/chain-rpc/
export NODE=http://89.169.111.79:8000/
```



### Genesis and chain status

```bash
curl -sS "$NODE_RPC/status" | jq '.result.sync_info | {catching_up, latest_block_height}'
```



### Supported models on testnet

List models currently accepted by governance:

```bash
curl -sS "$NODE/v1/governance/models" | jq
```

Example models (VRAM is approximate minimum; confirm live list with the curl above):


| Model ID                      | VRAM (GB) | Notes                        |
| ----------------------------- | --------- | ---------------------------- |
| `Qwen/Qwen2.5-7B-Instruct`    | 24        | tool-calling / Hermes parser |
| `Qwen/Qwen3-4B-Instruct-2507` | 24        | tool-calling / Hermes parser |
| `MiniMaxAI/MiniMax-M2.7`      | 320       | large context                |
| `moonshotai/Kimi-K2.6`        | 720       | large context                |


Pick a model that fits your GPU memory and configure it in `node-config.json` (see step 5).

---



## Deployment overview

```text
v0.2.16 CLI (local)             → cold account key → ACCOUNT_PUBKEY in config.env (stays offline)
v0.2.17-testnet-1 inferenced    → cosmovisor/upgrades/v0.2.17 (required before first node start)
v0.2.17-testnet-1 decentralized-api → .dapi/cosmovisor/upgrades/v0.2.17 (required before first api start)
Docker node image (v0.2.15)     → ships /usr/bin/inferenced only; not enough after the v0.2.16 upgrade
config.env + overrides          → testnet + state sync; KEY_NAME = warm ML key name on server
step 14.1 (api container)       → keys add KEY_NAME (warm); read consensus key from node
step 14.2 (fund cold)           → cold account must exist on chain before it can sign anything
step 14.3 (local CLI, cold key) → tx inference submit-new-participant (HTTP /v1/participants is gone in v0.2.17 → 410)
step 15 (local CLI, cold key)   → grant-ml-ops-permissions cold → warm (cosmos group: --gas-prices 1ngonka)
FIRST_RUN init                  → genesis download + [statesync] enabled
state sync                      → snapshot at current height (post-upgrade), then block sync
api / proxy / mlnode            → inference after registration + grants
```

---



## Step-by-step



### 1. Download CLI v0.2.16 (key creation)

On your **local machine** (or any host where you generate keys), download **inferenced v0.2.16** from:

[https://github.com/gonka-ai/gonka/releases/tag/release/v0.2.16](https://github.com/gonka-ai/gonka/releases/tag/release/v0.2.16)

Follow [Create account key](https://gonka.ai/docs/host/quickstart/#local-machine-create-account-key) to create your **cold account key** (pick any keyring name, e.g. `my-account`). Save:

- `ACCOUNT_PUBKEY` (base64 public key from `inferenced keys show <cold-key-name> --pubkey`)
- `KEYRING_PASSWORD` (for the cold key — you will reuse this on the server for the warm key in step 14)
- The cold key address (`gonka1…`) — **must be funded before registration** (step 14.2). On v0.2.17 registration is an on-chain tx signed by the cold key, and an account that has never received funds does not exist on chain (`account not found`). The same balance later pays HardwareDiff / PoC fees via feegrant.

**Keep the cold key offline.** Do **not** import it into the API container. Step 14 creates a **separate warm ML operational key** (`KEY_NAME` in `config.env`); its pubkey will **not** match `ACCOUNT_PUBKEY` — that is expected.

### 2. Clone the deploy branch

```bash
git clone https://github.com/gonka-ai/gonka.git
cd gonka/deploy/join
```



### 3. Create `config.env`

You can use a template from [quickstart](https://gonka.ai/docs/host/quickstart/#local-machine-create-account-key) and replace placeholders with your values.

For the testnet variables use the information below (testnet seed at `89.169.111.79`):

```bash
# Warm ML operational key name (created on the server in step 14 — not the offline cold key).
export KEY_NAME="join-18221"
export KEYRING_PASSWORD="<your-password>"
export API_PORT="8000"
# Must be unique on chain — query the seed before choosing a port:
# curl -sS "$NODE/v1/participants" | jq '.[] | select(.inferenceUrl | contains("<your-domain-url>"))'
export PUBLIC_URL="http://<your-domain-url>:19242"
export P2P_EXTERNAL_ADDRESS="tcp://<your-domain-url>:19241"
export ACCOUNT_PUBKEY="<your-account-pub-key-from-previous-step>"
export NODE_CONFIG="./node-config.json"
export HF_HOME="/srv/dai/cache/"
export SEED_API_URL="http://89.169.111.79:8000"
export SEED_NODE_RPC_URL="http://89.169.111.79:8000/chain-rpc/"
export SEED_NODE_P2P_URL="tcp://89.169.111.79:5000"
export BEACON_STATE_URL="https://sepolia.checkpoint-sync.ethpandaops.io"
export DAPI_API__POC_CALLBACK_URL="http://172.18.114.112:9100"
export DAPI_CHAIN_NODE__URL="http://node:26657"
export DAPI_CHAIN_NODE__P2P_URL="http://node:26656"
export RPC_SERVER_URL_1="http://89.169.111.79:8000/chain-rpc/"
export RPC_SERVER_URL_2="http://89.169.111.79:8000/chain-rpc/"
export NODE_RPC_URL="http://127.0.0.1:26657"
export PORT="8080"
export INFERENCE_PORT="5050"
export KEYRING_BACKEND="file"

# Observability (docs/observability/observability-overview.md)
# Jaeger and Grafana UIs are disabled by default. Set credentials below first,
# then set JAEGER_ENABLED=true and/or GRAFANA_ENABLED=true to expose them via proxy.
export JAEGER_ENABLED=false
export GRAFANA_ENABLED=false
export JAEGER_BASIC_AUTH_USER=jaeger
export JAEGER_BASIC_AUTH_PASSWORD=<FILLIN>
export GRAFANA_ADMIN_USER=admin
export GRAFANA_ADMIN_PASSWORD=<FILLIN>
export DAPI_OTEL_ENABLED=true
export DEVSHARD_OTEL_ENABLED=true
export OTEL_ENDPOINT=http://jaeger:4317
# CometBFT Prometheus metrics on node:26660 (gonka-node scrape job)
export NODE_INSTRUMENTATION_PROMETHEUS=true

# Transaction fee gas price (optional override).
# Leave at 0 (default): DAPI auto-reads FeeParams from chain at startup.
# Do NOT set this to 1 thinking it fixes post-v0.2.16 fees — the current
# api:0.2.15-post3 image ignores non-zero values and only looks at the
# top-level min_gas_price_ngonka field (often 0 even when fee groups are on).
export DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA=0

# Query gas limit (protects from expensive read queries)
export QUERY_GAS_LIMIT=10000000

export SYNC_WITH_SNAPSHOTS="true"
export SNAPSHOT_INTERVAL="200"
export IS_TEST_NET="true"
export ETHEREUM_NETWORK="sepolia"
export CHAIN_ID="gonka-testnet"
export TX_GAS_PRICES=""
export GRANT_MIN_SPENDABLE_NGONKA="20000000000"
export JOIN_FUND_WAIT_SECONDS="600"

export POSTGRES_HOST="postgres"
export POSTGRES_PORT="5432"
export POSTGRES_DB="payloads"
export POSTGRES_USER="payloads"
export POSTGRES_PASSWORD="payloads"

export BOUNTY_POOL_ENABLED="true"
export BOUNTY_POOL_IBC_DENOM="ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4"
export BOUNTY_POOL_CHAIN_ID="kava_2222-10"
export BOUNTY_POOL_NAME="USDT"
export BOUNTY_POOL_SYMBOL="USDT"
export BOUNTY_POOL_DECIMALS="6"
export BOUNTY_POOL_AMOUNT="1500000000000"
export BOUNTY_POOL_COMMUNITY_SALE_LABEL="community-sale-testnet-v1"
export BOUNTY_POOL_GOV_AUTHORITY="gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33"
export WRAPPED_TOKEN_SETUP_ENABLED="true"
```

**Important**

- Use the seed **proxy** RPC path `.../chain-rpc/` on port **8000**, not `:26657` on the seed host.
- `DAPI_API__POC_CALLBACK_URL` must be reachable from your node (often the host LAN IP and port `9100`).
- Trailing slash on `SEED_NODE_RPC_URL` and `RPC_SERVER_URL_*` is required.
- Since **v0.2.16** the chain enforces fees via `fee_params.enabled_fee_groups` even when top-level `min_gas_price_ngonka` is `0`. On **gonka-testnet (v0.2.17-testnet-1)** the enabled groups are `epoch`, `cosmos` and `wasm`, each at `min_gas_price=1` ngonka/gas (`wasm` is enabled by the v0.2.17 upgrade handler). Check before going live:

```bash
curl -sS "$NODE/chain-api/productscience/inference/inference/params" \
  | jq '.params.fee_params | {min_gas_price_ngonka, enabled_fee_groups, groups: [.groups[]? | {name, min_gas_price}]}'
# expect enabled_fee_groups: ["epoch","cosmos","wasm"]
```

What this means for the join flow:

| Group | Messages you will send | Fee |
| --- | --- | --- |
| `onboarding` (disabled) | `MsgSubmitNewParticipant` (step 14.3) | free — but the cold account must already exist (be funded) to sign |
| `cosmos` | `MsgGrant`, `MsgGrantAllowance` (`grant-ml-ops-permissions`, step 15), `tx bank send`, feegrant revoke | **paid** — pass `--gas-prices 1ngonka` or the tx is rejected with `insufficient fee` |
| `epoch` | `MsgSubmitHardwareDiff`, `MsgPoCV2StoreCommit`, collateral, delegation (sent by DAPI with the warm key / by you from cold) | paid — DAPI pays via the cold→warm feegrant |
| `wasm` | any `tx wasm …` (bridge wrap/unwrap, community sale) | paid — except the liquidity-pool `purchase_with_native` swap with an approved IBC token, which is fee-bypassed (gas ≤ 500000) |

Ensure **chain and API/DAPI upgrade together** (same release line as the node binary). After `docker compose up -d api`, confirm logs do **not** repeat `insufficient fee` — if they do, the API binary is too old for the chain’s fee rules.

### 4. Prepare `node-config.json`

Edit `node-config.json` for the models you will serve. Example (single GPU node running Qwen 2.5 7B):

```json
[
  {
    "id": "node1",
    "host": "inference",
    "inference_port": 5000,
    "poc_port": 8080,
    "max_concurrent": 500,
    "models": {
      "Qwen/Qwen2.5-7B-Instruct": {
        "args": [
          "--enable-auto-tool-choice",
          "--tool-call-parser",
          "hermes",
          "--max-model-len",
          "4096",
          "--gpu-memory-utilization", 
          "0.92"
        ]
      }
    }
  }
]
```

See `deploy/join/node-config.json` in the repo for multi-model examples.

### 5. Docker Compose overrides

Create `docker-compose.env-override.yml` so containers get testnet `CHAIN_ID` / `COIN_DENOM` (compose does not set these on `node` by default):

```bash
cat > docker-compose.env-override.yml <<'EOF'
services:
  tmkms:
    environment:
      - IS_TEST_NET=true
      - CHAIN_ID=gonka-testnet
  node:
    environment:
      - IS_TEST_NET=true
      - CHAIN_ID=gonka-testnet
  api:
    environment:
      - IS_TEST_NET=true
      - ENFORCED_MODEL_ID=Qwen/Qwen3-4B-Instruct-2507
      - ENFORCED_MODEL_ARGS=--enable-auto-tool-choice --tool-call-parser hermes --max-model-len 25000
  proxy:
    environment:
      - IS_TEST_NET=true
      - DISABLE_GONKA_API=false
      - DISABLE_CHAIN_API=false
      - DISABLE_CHAIN_RPC=false
      - DISABLE_CHAIN_GRPC=false
  proxy-ssl:
    environment:
      - IS_TEST_NET=true
  explorer:
    environment:
      - IS_TEST_NET=true
  bridge:
    environment:
      - ETHEREUM_NETWORK=sepolia
      - BEACON_STATE_URL=https://sepolia.checkpoint-sync.ethpandaops.io
EOF
```

Adjust `ENFORCED_MODEL_*` only if you intentionally force a specific model in API; otherwise align with your `node-config.json`.

### 6. Pull images

```bash
set -a && source ./config.env && set +a

docker compose -f docker-compose.yml -f docker-compose.mlnode.yml pull
```



### 7. Confirm the chain upgrade you must run

From the deploy directory, use the seed URLs (same as Prerequisites — trailing slash on `$NODE` is required):

```bash
export NODE_RPC=http://89.169.111.79:8000/chain-rpc/
export NODE=http://89.169.111.79:8000/

curl -sS "$NODE_RPC/status" | jq '.result.sync_info.latest_block_height'
curl -sS "${NODE}chain-api/cosmos/upgrade/v1beta1/applied_plan/v0.2.16" | jq
curl -sS "${NODE}chain-api/cosmos/upgrade/v1beta1/current_plan" | jq
```



### 8. Download v0.2.17-testnet-1 binaries (node + API)

Use the **on-chain upgrade artifacts** from [race-releases](https://github.com/product-science/race-releases/releases/tag/release%2Fv0.2.17-testnet-1) `v0.2.17-testnet-1` — not the generic GitHub Gonka release alone. Run in `deploy/join`:

```bash
curl -fL -o inferenced-amd64.zip \
  "https://github.com/product-science/race-releases/releases/download/release%2Fv0.2.17-testnet-1/inferenced-amd64.zip"
curl -fL -o decentralized-api-amd64.zip \
  "https://github.com/product-science/race-releases/releases/download/release%2Fv0.2.17-testnet-1/decentralized-api-amd64.zip"

sha256sum inferenced-amd64.zip decentralized-api-amd64.zip
# expect inferenced-amd64.zip:
#   sha256:b596137cc16fad091fead853195a80d12ced14e237b9327613dcbf2b3662f4a7
# expect decentralized-api-amd64.zip:
#   sha256:2a40ccc7ce609c32a7d82510d6e280f9fbb89308858843f794ec88b767ba230f

unzip -o inferenced-amd64.zip
# extracts: inferenced, libgcc_s.so.1, libwasmvm_muslc.x86_64.a, wrapped_token.wasm
# (keep decentralized-api-amd64.zip for step 9 — do not unzip on the host)
```

**When:** install **both** binaries before the matching container’s first start (node → step 11, api → step 14/16). The Docker images (`inferenced:0.2.15`, `api:0.2.15-post3`) are only bootstrap shells; cosmovisor runs the zips from the host volumes.

The binary is **musl**-linked (for the node container). Do **not** expect `./inferenced version` to work on a glibc host — it fails with `No such file or directory`. Verify with Docker instead:

```bash
docker run --rm --entrypoint /bin/sh \
  -v "$(pwd)/inferenced:/tmp/inferenced:ro" \
  ghcr.io/product-science/inferenced:0.2.15 \
  -c '/tmp/inferenced version'
# expect: v0.2.17-testnet-1
```



### 9. Pre-install Cosmovisor layout (before first node start)

The node image runs cosmovisor. On first start, `init-docker.sh` calls `cosmovisor init /usr/bin/inferenced` (v0.2.15) **only when** `.inference/cosmovisor/` does not exist. Pre-create the directory and point `current` at **v0.2.17** so state sync never replays post-upgrade blocks with the wrong binary.

`.inference/` is typically root-owned — use `sudo` for all paths under it.

```bash
sudo mkdir -p .inference/cosmovisor/upgrades/v0.2.17/bin
sudo install -m 0755 ./inferenced .inference/cosmovisor/upgrades/v0.2.17/bin/inferenced

# Runtime binary for sync (required).
sudo ln -sfn upgrades/v0.2.17 .inference/cosmovisor/current

# Optional: same binary in genesis for cosmovisor tooling.
sudo mkdir -p .inference/cosmovisor/genesis/bin
sudo install -m 0755 ./inferenced .inference/cosmovisor/genesis/bin/inferenced

# Cosmovisor requires data/ to exist before start.
sudo mkdir -p .inference/data .inference/wasm

sudo readlink .inference/cosmovisor/current
# expect: upgrades/v0.2.17
```

Verify the installed binary **inside** a node container mount (host cannot execute musl binaries):

```bash
docker run --rm --entrypoint /bin/sh \
  -v "$(pwd)/.inference:/root/.inference:ro" \
  ghcr.io/product-science/inferenced:0.2.16 \
  -c '/root/.inference/cosmovisor/current/bin/inferenced version --home /tmp'
# expect: v0.2.17-testnet-1
# (--home /tmp avoids mkdir under the read-only mount)
```

**Do not** run step 11 until both node checks pass.

#### DAPI cosmovisor (before first `api` start)

The `api` container also runs cosmovisor over the host-mounted `.dapi/` volume. Pre-install **v0.2.17** so the API understands post-upgrade chain fee rules (fee groups). Use the **same upgrade name** as the node: `v0.2.17`.

`.dapi/` is typically root-owned — use `sudo`.

```bash
sudo mkdir -p .dapi/cosmovisor/upgrades/v0.2.17/bin
sudo unzip -o decentralized-api-amd64.zip -d .dapi/cosmovisor/upgrades/v0.2.17/bin
sudo chmod +x .dapi/cosmovisor/upgrades/v0.2.17/bin/decentralized-api

sudo ln -sfn upgrades/v0.2.17 .dapi/cosmovisor/current

sudo readlink .dapi/cosmovisor/current
# expect: upgrades/v0.2.17
```

Verify the installed binary (DAPI has no standalone `version` CLI — it always opens SQLite — so use `strings`):

```bash
sudo strings .dapi/cosmovisor/current/bin/decentralized-api \
  | grep -E '^v0\.2\.17' | head -3
# expect: v0.2.17-testnet-1
```

After `api` is running, prefer:

```bash
curl -sS http://127.0.0.1:8000/v1/versions | jq '{node:.node_version.version, api:.api_version.version}'
# expect both v0.2.17-testnet-1
```



### 11. Start TMKMS and node

```bash
set -a && source ./config.env && set +a

sudo readlink .inference/cosmovisor/current
# expect: upgrades/v0.2.17

docker compose -f docker-compose.yml \
  -f docker-compose.mlnode.yml \
  -f docker-compose.env-override.yml \
  up -d tmkms node
```

After the container is up, confirm the running binary:

```bash
docker exec node /root/.inference/cosmovisor/current/bin/inferenced version
# expect: v0.2.17-testnet-1
```

On first start, `init-docker.sh` should:

1. Initialize config (if missing)
2. Download genesis from the seed
3. Enable state sync and set trusted block (`TRUSTED_BLOCK_PERIOD=2000`)
4. Create `.node_initialized`
5. **Not** re-run `cosmovisor init` (`.inference/cosmovisor/` already exists from step 9)

**Healthy logs** include:

```text
Starting state sync
Discovering snapshots
Snapshot restored ... height=...
Time to switch to consensus reactor!
finalized block height=...
ABCI Handshake App Info ... software-version=0.2.17-testnet-1
```



### 12. Verify sync

RPC `26657` is usually **not** published on the host for join compose (only P2P is). Query via `docker exec`:

```bash
docker exec node wget -qO- http://127.0.0.1:26657/status \
  | jq '.result.sync_info | {catching_up, latest_block_height}'

curl -sS "http://89.169.111.79:8000/chain-rpc/status" \
  | jq -r '.result.sync_info.latest_block_height'
```

Wait until local `catching_up` is `false` and height is close to the seed.

Optional: confirm state sync block in config:

```bash
sudo grep -A12 '^\[statesync\]' .inference/config/config.toml
```

Expect `enable = true`, `rpc_servers`, `trust_height`, and `trust_hash`.

### 13. Pre-download model weights

Before serving traffic, cache Hugging Face weights on the host path set by `HF_HOME` (quickstart reference: [Pre-download model weights](https://gonka.ai/docs/host/quickstart/#server-pre-download-model-weights-to-hugging-face-cache-hf_home)).

The quickstart assumes `huggingface-cli` is installed. Fresh filfox hosts have **neither `huggingface-cli` nor `pip`** (`/usr/bin/python3: No module named pip`). Use one of these:

**Option A — no install, use the mlnode image already pulled in step 6** (preferred):

```bash
set -a && source ./config.env && set +a
mkdir -p "$HF_HOME"
IMG=$(grep -oE 'ghcr.io/gonka-ai/mlnode:[^" ]+' docker-compose.mlnode.yml | head -1); echo "$IMG"
docker run --rm -v "$HF_HOME":/hf -e HF_HOME=/hf --entrypoint python3 "$IMG" \
  -c 'from huggingface_hub import snapshot_download; print(snapshot_download("Qwen/Qwen3-4B-Instruct-2507"))'
```

**Option B — install the CLI** (needs sudo):

```bash
sudo apt-get update && sudo apt-get install -y python3-pip
python3 -m pip install --user -U "huggingface_hub[cli]"   # add --break-system-packages on Ubuntu 24.04
export PATH="$HOME/.local/bin:$PATH"
hf download Qwen/Qwen3-4B-Instruct-2507                    # `huggingface-cli download …` still works as alias
```

Verify the cache is where `mlnode-308` mounts it (`HF_HOME` in `config.env` must match the volume in `docker-compose.mlnode.yml`):

```bash
ls "$HF_HOME/hub/models--Qwen--Qwen3-4B-Instruct-2507/snapshots/"
```

### 14. Key setup and participant registration

The quickstart’s [3. Complete key setup and host registration](https://gonka.ai/docs/host/quickstart/#3-complete-key-setup-and-host-registration) still describes the pre-v0.2.17 flow. **On v0.2.17 the API-side registration is removed**: `inferenced register-new-participant` (HTTP `POST /v1/participants`, `MsgSubmitNewUnfundedParticipant`) returns

```text
server returned status 410: {"error":"deprecated","message":"POST /v1/participants is deprecated; fund your account and register on-chain with MsgSubmitNewParticipant"}
```

The quickstart’s “manual registration flow” is now the **only** flow. Order matters: fund cold → register on-chain from cold → grant.

#### 14.1 Create the warm ML key and read the consensus key (server, `api` container)

```bash
set -a && source ./config.env && set +a
docker compose -f docker-compose.yml -f docker-compose.mlnode.yml -f docker-compose.env-override.yml \
  run --rm --no-deps -it api /bin/sh

# inside the container
printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | inferenced keys add "$KEY_NAME" --keyring-backend file
# note the warm address (gonka1…) and SAVE THE MNEMONIC

curl -s $DAPI_CHAIN_NODE__URL/status | jq -r '.result.validator_info.pub_key.value'
# note the consensus key (base64) — needed in 14.3
exit
```

Running `register-new-participant` here still works as a dry run that prints the cold address, cold pubkey and auto-fetched consensus key before failing with 410 — useful to copy the values, but it does not register anything.

#### 14.2 Fund the cold account (any funded testnet1 key)

The cold account must exist on chain before it can sign the registration tx. 10 GNK covers registration, the grant and several epochs of fees:

```bash
# from a funded testnet1 key, e.g. gonka-account-key on 89.169.111.79 (/srv/dai)
./inferenced tx bank send gonka-account-key <cold-address> 10000000000ngonka \
  --keyring-backend file --keyring-dir .inference --chain-id gonka-testnet \
  --node http://localhost:8000/chain-rpc/ --gas 200000 --gas-prices 1ngonka -y

curl -sS "http://89.169.111.79:8000/chain-api/cosmos/bank/v1beta1/balances/<cold-address>" | jq
```

#### 14.3 Register on-chain (local machine, cold key)

```bash
./inferenced tx inference submit-new-participant \
    <PUBLIC_URL> \
    --validator-key <consensus-key-from-14.1> \
    --from <cold-key-name> \
    --keyring-backend file \
    --chain-id gonka-testnet \
    --node http://89.169.111.79:8000/chain-rpc/ \
    --gas 400000 --gas-prices 1ngonka \
    --timeout-duration 1m --unordered
```

`MsgSubmitNewParticipant` is in the disabled `onboarding` group, so it is free; the `--gas-prices` flag is harmless (≤ 0.0004 GNK). Add `--keyring-dir <dir>` if the cold key was created with a custom one. Confirm:

```bash
curl -sS "http://89.169.111.79:8000/v2/participants/<cold-address>" | jq
```

#### 14.4 Grant ML ops permissions

Step 15 below — cold key on your **local** machine → warm address from 14.1.

**Testnet reminders** (not in the mainnet-oriented quickstart wording):


| Key                  | Where                           | Used for                                                                 |
| -------------------- | ------------------------------- | ------------------------------------------------------------------------ |
| **Cold account key** | Offline (step 1)                | Participant owner; `ACCOUNT_PUBKEY` in `config.env`                      |
| **Warm ML key**      | Server (`keys add "$KEY_NAME"`) | Day-to-day signing after grant; pubkey **≠** `ACCOUNT_PUBKEY` (expected) |


- Confirm `PUBLIC_URL` is unique before registration (query the seed).
- The cold account is funded in **14.2**, before registration. That balance also pays, via feegrant, the routine `epoch`-group msgs DAPI sends with the warm key (`MsgSubmitHardwareDiff`, `MsgPoCV2StoreCommit`; `min_gas_price=1`). A few million `ngonka` is enough for early diffs; keep ≥ `GRANT_MIN_SPENDABLE_NGONKA` for PoC `StoreCommit` headroom.
- After 14.3, confirm on the **testnet** seed:

```bash
curl -sS "http://89.169.111.79:8000/v1/participants" \
  | jq '.[] | select(.inferenceUrl == "http://<your-domain-url>:19242")'
```



### 15. Grant ML operational key permissions

Same as quickstart **[3.3](https://gonka.ai/docs/host/quickstart/#33-local-machine-grant-permissions-to-ml-operational-key)** — run on your **local** machine with the cold key; grantee is the warm `gonka1…` address from 14.1:

```bash
./inferenced tx inference grant-ml-ops-permissions <cold-key-name> <warm-address> \
  --from <cold-key-name> --keyring-backend file --chain-id gonka-testnet \
  --node http://89.169.111.79:8000/chain-rpc/ \
  --gas 2000000 --gas-prices 1ngonka
```

That tx grants authz **and** a 10 GNK feegrant (cold → warm). Required before the API can sign host duties. `MsgGrant` / `MsgGrantAllowance` are in the **`cosmos` fee group (enabled, 1 ngonka/gas)** — the grant is **not** free on testnet; without `--gas-prices 1ngonka` it is rejected with `insufficient fee`. Cost at `--gas 2000000` is 0.002 GNK.

### 16. Start the full stack

```bash
set -a && source ./config.env && set +a

docker compose -f docker-compose.yml \
  -f docker-compose.mlnode.yml \
  -f docker-compose.env-override.yml \
  up -d
```

Omit `docker-compose.mlnode.yml` if GPUs or drivers are not ready (CUDA 12.9+ for current `mlnode` images).

### 17. Verify operation

1. Open your dashboard: `http://<your-host>:<api-port>/dashboard/gonka/validator` (path may vary; use your `PUBLIC_URL`).
2. Confirm you appear alongside other participants.
3. Check container logs: `docker compose ... logs -f node api proxy mlnode-308`
4. Confirm API startup log does not show repeating `insufficient fee` (fee mismatch between API image and chain `fee_params`).
5. Confirm vLLM is loading the model(s) from `node-config.json` (match the [supported models](#supported-models-on-testnet) list).

---



## Compose file reference

Always pass the same three files:

```bash
docker compose -f docker-compose.yml \
  -f docker-compose.mlnode.yml \
  -f docker-compose.env-override.yml \
  <command>
```

Source `config.env` before any `docker compose` command:

```bash
set -a && source ./config.env && set +a
```

---



## Checklist

- [ ] v0.2.16 CLI used for **cold account** key; `ACCOUNT_PUBKEY` in `config.env` matches local `keys show` output
- [ ] `PUBLIC_URL` unique on chain
- [ ] v0.2.17-testnet-1 installed under `.inference/cosmovisor/upgrades/v0.2.17/bin/inferenced`
- [ ] v0.2.17-testnet-1 DAPI under `.dapi/cosmovisor/upgrades/v0.2.17/bin/decentralized-api`
- [ ] `cosmovisor/current` → `upgrades/v0.2.17` **before** first `node` start
- [ ] `/v1/versions` shows **node and api** both `v0.2.17-testnet-1`
- [ ] `CHAIN_ID=gonka-testnet`, `COIN_DENOM=ngonka`, `TRUSTED_BLOCK_PERIOD=2000`
- [ ] Seed RPC URLs use `http://89.169.111.79:8000/chain-rpc/`
- [ ] `catching_up: false` on local node
- [ ] Weights in `HF_HOME` (step 13, mlnode-image or pip method); vLLM serves chosen model
- [ ] Warm ML key created in 14.1 (`KEY_NAME`); pubkey **differs** from `ACCOUNT_PUBKEY` (expected); mnemonic saved
- [ ] Cold account **funded before registration** (14.2); `/chain-api/cosmos/bank/v1beta1/balances/<cold>` non-empty
- [ ] Participant registered **on-chain** with `submit-new-participant` (14.3) — not via `register-new-participant` (410 on v0.2.17)
- [ ] ML ops permissions granted cold → warm with `--gas-prices 1ngonka` (step 15, `cosmos` group)
- [ ] `enabled_fee_groups` = `epoch`,`cosmos`,`wasm`; no repeating `insufficient fee` in `docker logs api`

---



## Related docs

- [Host quickstart](https://gonka.ai/docs/host/quickstart/)
- [Join chain (legacy)](join_chain.md)
- Deploy templates: `deploy/join/config.env.template`, `deploy/join/node-config.json`

