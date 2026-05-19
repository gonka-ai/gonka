# Gonka testnet — host join guide

Use this document to bring up a new host from scratch and join **gonka-testnet**: sync the chain via state sync, register as a participant, and serve inference.

## Quick links

| Resource | URL |
|----------|-----|
| Validator dashboard (seed) | http://89.169.111.79:8000/dashboard/gonka/validator |
| Chain binary (on-chain) | [inferenced v0.2.13](https://github.com/gonka-ai/gonka/releases/tag/release/v0.2.13) |
| CLI for keygen (off-chain) | [inferenced v0.2.12](https://github.com/gonka-ai/gonka/releases/tag/release/v0.2.12) |
| Official quickstart | https://gonka.ai/docs/host/quickstart/ |
| Repo branch for deploy | [gm/microrelease](https://github.com/gonka-ai/gonka/tree/gm/microrelease) |

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

| Model ID | VRAM (GB) | Notes |
|----------|-----------|--------|
| `Qwen/Qwen2.5-7B-Instruct` | 24 | `--quantization fp8` |
| `Qwen/Qwen3-4B-Instruct-2507` | 24 | tool-calling / Hermes parser |
| `MiniMaxAI/MiniMax-M2.7` | 320 | large context |
| `moonshotai/Kimi-K2.6` | 720 | large context |

Pick a model that fits your GPU memory and configure it in `node-config.json` (see step 5).

---

## Deployment overview

```text
v0.2.12 CLI (host)     → account key + ACCOUNT_PUBKEY
v0.2.13 inferenced     → cosmovisor/genesis/bin (chain sync)
config.env + overrides → testnet + state sync
FIRST_RUN init         → genesis download + [statesync] enabled
state sync             → snapshot ~11000, then block sync
api / proxy / mlnode   → registration + inference
```

---

## Step-by-step

### 1. Download CLI v0.2.12 (key creation)

On your **local machine** (or any host where you generate keys), download **inferenced v0.2.12** from:

https://github.com/gonka-ai/gonka/releases/tag/release/v0.2.12

Follow [Create account key](https://gonka.ai/docs/host/quickstart/#local-machine-create-account-key) to produce:

- `KEY_NAME`
- `ACCOUNT_PUBKEY` (base64 public key)
- `KEYRING_PASSWORD`

Keep the cold key offline; you will import or recreate the key in the API container later (step 16).

### 2. Clone the deploy branch

```bash
git clone https://github.com/gonka-ai/gonka.git -b gm/microrelease
cd gonka/deploy/join
```

### 3. Create `config.env`

You can use a template from [quickstart](https://gonka.ai/docs/host/quickstart/#local-machine-create-account-key) and replace placeholders with your values.

For the testnet variables use the information below (testnet seed at `89.169.111.79`):

```bash
export KEY_NAME="join-18217"
export KEYRING_PASSWORD="<your-password>"
export API_PORT="8000"
export PUBLIC_URL="http://your-host.example:19234"
export P2P_EXTERNAL_ADDRESS="tcp://your-host.example:19233"
export ACCOUNT_PUBKEY="<your-base64-pubkey>"
export NODE_CONFIG="./node-config.json"
export HF_HOME="/srv/dai/cache/"

export SEED_API_URL="http://89.169.111.79:8000"
export SEED_NODE_RPC_URL="http://89.169.111.79:8000/chain-rpc/"
export SEED_NODE_P2P_URL="tcp://89.169.111.79:5000"
export RPC_SERVER_URL_1="http://89.169.111.79:8000/chain-rpc/"
export RPC_SERVER_URL_2="http://89.169.111.79:8000/chain-rpc/"

export DAPI_API__POC_CALLBACK_URL="http://<your-host-lan-ip>:9100"
export DAPI_CHAIN_NODE__URL="http://node:26657"
export DAPI_CHAIN_NODE__P2P_URL="http://node:26656"

export PORT="8080"
export INFERENCE_PORT="5050"
export KEYRING_BACKEND="file"
export SYNC_WITH_SNAPSHOTS="true"
export SNAPSHOT_INTERVAL="200"
export IS_TEST_NET="true"
export ETHEREUM_NETWORK="sepolia"
export BEACON_STATE_URL="https://sepolia.checkpoint-sync.ethpandaops.io"
export CHAIN_ID="gonka-testnet"
export COIN_DENOM="ngonka"
export TRUSTED_BLOCK_PERIOD=2000
```

**Important**

- Use the seed **proxy** RPC path `.../chain-rpc/` on port **8000**, not `:26657` on the seed host.
- `DAPI_API__POC_CALLBACK_URL` must be reachable from your node (often the host LAN IP and port `9100`).
- Trailing slash on `SEED_NODE_RPC_URL` and `RPC_SERVER_URL_*` is required.

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
          "--quantization",
          "fp8",
          "--gpu-memory-utilization",
          "0.8"
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
      - COIN_DENOM=ngonka
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
  explorer:
    environment:
      - IS_TEST_NET=true
EOF
```

Adjust `ENFORCED_MODEL_*` only if you intentionally force a specific model in API; otherwise align with your `node-config.json`.

### 6. Pull images

```bash
set -a && source ./config.env && set +a

docker compose -f docker-compose.yml -f docker-compose.mlnode.yml pull
```

### 7. Install chain binary v0.2.13 (Cosmovisor)

```bash
curl -fL -o inferenced-amd64.zip \
  "https://github.com/gonka-ai/gonka/releases/download/release/v0.2.13/inferenced-amd64.zip"

unzip -o inferenced-amd64.zip
chmod +x inferenced   # name may be inferenced-amd64 depending on zip layout; rename if needed
```

### 8. Place binary for the node container

```bash
sudo mkdir -p .inference/cosmovisor/genesis/bin
sudo cp ./inferenced .inference/cosmovisor/genesis/bin/inferenced
sudo chmod +x .inference/cosmovisor/genesis/bin/inferenced
```

The node image ships an older binary; **v0.2.13** in `genesis/bin` is required for testnet height and upgrades.

### 9. Clean first-run state (new host or re-join)

```bash
docker compose -f docker-compose.yml \
  -f docker-compose.mlnode.yml \
  -f docker-compose.env-override.yml \
  stop node tmkms 2>/dev/null || true

sudo rm -rf .inference/config
sudo rm -f .inference/.node_initialized

sudo rm -rf .inference/data .inference/wasm
sudo mkdir -p .inference/data .inference/wasm
```

Do **not** leave `data/` missing — Cosmovisor requires the directory to exist.

### 10. Start TMKMS and node

```bash
set -a && source ./config.env && set +a

docker compose -f docker-compose.yml \
  -f docker-compose.mlnode.yml \
  -f docker-compose.env-override.yml \
  up -d tmkms node
```

On first start, `init-docker.sh` should:

1. Initialize config (if missing)
2. Download genesis from the seed
3. Enable state sync and set trusted block (`TRUSTED_BLOCK_PERIOD=2000`)
4. Create `.node_initialized`

**Healthy logs** include:

```text
Starting state sync
Discovering snapshots
Snapshot restored ... height=11000
Time to switch to consensus reactor!
finalized block height=...
```

**Failure signs** (stop and repeat step 9):

- `InitChain` + panic on `tokenomics_params` (genesis replay without state sync)
- `upgrade handler is missing for v0.2.13` (wrong binary in cosmovisor)
- `data must be an existing directory` (removed `data/` without `mkdir`)

### 11. Verify sync

```bash
curl -s http://localhost:26657/status | jq '.result.sync_info | {catching_up, latest_block_height}'

curl -s "http://89.169.111.79:8000/chain-rpc/status" | jq -r '.result.sync_info.latest_block_height'
```

Wait until local `catching_up` is `false` and height is close to the seed.

Optional: confirm state sync block in config:

```bash
sudo grep -A12 '^\[statesync\]' .inference/config/config.toml
```

Expect `enable = true`, `rpc_servers`, `trust_height`, and `trust_hash`.

### 12. Pre-download model weights

Before serving traffic, cache Hugging Face weights on the host path set by `HF_HOME`:

https://gonka.ai/docs/host/quickstart/#server-pre-download-model-weights-to-hugging-face-cache-hf_home

### 13. Key setup and participant registration

Follow [Complete key setup and host registration](https://gonka.ai/docs/host/quickstart/#3-complete-key-setup-and-host-registration).

Enter the API container:

```bash
docker compose -f docker-compose.yml \
  -f docker-compose.mlnode.yml \
  -f docker-compose.env-override.yml \
  run --rm --no-deps -it api /bin/sh
```

Inside the container (replace password and ensure env vars are set — compose maps `PUBLIC_URL` → `DAPI_API__PUBLIC_URL` and `SEED_API_URL` → `DAPI_CHAIN_NODE__SEED_API_URL`):

```bash
printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | \
  inferenced keys add "$KEY_NAME" --keyring-backend file

inferenced register-new-participant \
  "$DAPI_API__PUBLIC_URL" \
  "$ACCOUNT_PUBKEY" \
  --node-address "$DAPI_CHAIN_NODE__SEED_API_URL"

exit
```

### 14. Grant ML operational key permissions

https://gonka.ai/docs/host/quickstart/#33-local-machine-grant-permissions-to-ml-operational-key

### 15. Start the full stack

```bash
set -a && source ./config.env && set +a

docker compose -f docker-compose.yml \
  -f docker-compose.mlnode.yml \
  -f docker-compose.env-override.yml \
  up -d
```

Omit `docker-compose.mlnode.yml` if GPUs or drivers are not ready (CUDA 12.9+ for current `mlnode` images).

### 16. Verify operation

1. Open your dashboard: `http://<your-host>:<api-port>/dashboard/gonka/validator` (path may vary; use your `PUBLIC_URL`).
2. Confirm you appear alongside other participants.
3. Check container logs: `docker compose ... logs -f node api proxy mlnode-308`
4. Confirm vLLM is loading the model(s) from `node-config.json` (match the [supported models](#supported-models-on-testnet) list).

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

- [ ] v0.2.12 CLI used for **account** key; `ACCOUNT_PUBKEY` in `config.env`
- [ ] v0.2.13 in `.inference/cosmovisor/genesis/bin/inferenced`
- [ ] `CHAIN_ID=gonka-testnet`, `COIN_DENOM=ngonka`, `TRUSTED_BLOCK_PERIOD=2000`
- [ ] Seed RPC URLs use `http://89.169.111.79:8000/chain-rpc/`
- [ ] First start: state sync completes past height ~11000
- [ ] `catching_up: false` on local node
- [ ] Participant registered; ML ops permissions granted
- [ ] Weights in `HF_HOME`; vLLM serves chosen model

---

## Related docs

- [Host quickstart](https://gonka.ai/docs/host/quickstart/)
- [Join chain (legacy)](join_chain.md)
- Deploy templates: `deploy/join/config.env.template`, `deploy/join/node-config.json`
