# Trainshard coordinator runbook

End-to-end: install `trainshardctl` locally, create and vote a training proposal (TSP), assemble a shard, run training, then settle.

Host-side daemon setup (`trainshardd`, mesh ports, hub/satellite) is summarized under [Prerequisites](#prerequisites-gpu-hosts). Full host detail: `[docs/trainshard-run.md](trainshard-run.md)`.

## Roles


| Role            | Who                                             | Tools                                                      |
| --------------- | ----------------------------------------------- | ---------------------------------------------------------- |
| **GPU hosts**   | Machines with `mlnode` + `trainshardd` opted in | Docker compose, `inferenced` on the host                   |
| **Coordinator** | Account that creates the TSP and drives the run | Local `trainshardctl` + `inferenced` (or a host with keys) |


The coordinator key that **creates** the TSP must be the same key `trainshardctl` uses for assemble / settle. Hosts only accept orders from that creator.

---



## 1. Build and install `trainshardctl` locally

From branch `feat/trainshards-v0-on-upgrade-v0.2.16` (this may change):

```bash
cd trainshard
make build
# produces:
#   trainshard/build/trainshardctl
#   trainshard/build/trainshardd

mkdir -p "$HOME/bin"
install -m 755 build/trainshardctl "$HOME/.local/bin/trainshardctl"
# ensure $HOME/bin is on PATH, or: export PATH="$PWD/build:$PATH"
trainshardctl --help
```

May need rebuilding when the code changes. Move the ctl close to the `inferenced` which you are planning to use. 

Commands: `assemble` · `prepare` · `deploy` · `start` · `status` · `logs` · `report` · `stop` · `settle` · `shell`.

On the same branch `feat/trainshards-v0-on-upgrade-v0.2.16` (this may change):

```bash
cd ../inference-chain
make build
```

Use the build for your local env - mac or linux. Move the resulting `inferenced` to the same location with `trainshardctl`.

---



## 2. Coordinator key

Get the TENDERMINT PRIVATE KEY from the team (Maria coordinates).
Put content of the key into a file for instance tsp-creator.pem. 

Then:

```bash
./inferenced keys import tsp-creator-key tsp-creator.pem \
  --keyring-backend file --home ".inference"

# Enter passphrase to decrypt your key: 12345678
# Enter keyring passphrase (attempt 1/3): 12345678
```

---



## 3. Coordinator environment

`trainshardctl` talks to the chain over **raw gRPC** (`host:port`). It cannot use an HTTP path like `/chain-rpc/`.

```bash
# Chain (testnet1 example)
export TRAINSHARDCTL_CHAIN_GRPC=89.169.111.79:9090   # or TRAINSHARD_CHAIN_GRPC=
export TRAINSHARDCTL_CHAIN_ID=gonka-testnet            # optional; checked when set

# Key that created / will create the TSP
export TRAINSHARDCTL_KEY_NAME=tsp-creator-key
export TRAINSHARDCTL_KEYRING_DIR=.inference      # or /srv/dai/.inference on a node
export TRAINSHARDCTL_KEYRING_BACKEND=file
export TRAINSHARDCTL_KEYRING_PASSWORD=12345678              # file backend

# Optional timeouts (defaults shown)
# export TRAINSHARDCTL_TIMEOUT=10m
# export TRAINSHARDCTL_CHAIN_TIMEOUT=30s
# export TRAINSHARDCTL_CHAIN_LANDING=2m
```

You can use either `TRAINSHARDCTL_*` or `TRAINSHARD_*` prefixes for the same names (`KEY_NAME`, `CHAIN_GRPC`, `KEYRING_DIR`, …).

Optional testnet1 starter file (gRPC only; add key/keyring yourself):
`[.local/trainshardctl-testnet79.env](../.local/trainshardctl-testnet79.env)` — `source` it, then set `TRAINSHARDCTL_KEY_*`.

For `inferenced` txs/queries on the same machine:

```bash
export CHAIN_ID=gonka-testnet
export NODE=http://89.169.111.79:8000/chain-rpc/   # HTTP RPC for inferenced
export KEY=tsp-creator-key
export HOME_INF=.inference

# Testnet typically needs gas prices
GAS=(--gas auto --gas-adjustment 1.5 --gas-prices 1ngonka --yes)
```

Confirm the key and balance:

```bash
./inferenced keys show "$KEY" -a --keyring-backend file --home "$HOME_INF"
./inferenced query bank balances "$(./inferenced keys show "$KEY" -a --keyring-backend file --home "$HOME_INF")" --node "$NODE" -o json
```

---



## Prerequisites (GPU hosts)

Before you propose / assemble:

1. Each host runs `trainshardd` (compose: `docker-compose.yml` + `docker-compose.trainshard.yml`, and hub/satellite overlays if multi-machine).
2. Public **proxy** must route `POST /trainshard` (image tag such as `0.2.15-trainshard`, not a proxy that returns 405 on those routes).
3. Nodes are **opted in** (daemon refreshes opt-in every few minutes). Opt-in TTL is short (~tens of blocks); keep `trainshardd` running.
4. GPU profile string on chain matches the proposal (`nvidia-smi` / hardware-nodes query), e.g. `NVIDIA GEFORCE RTX 3090 | 24GB x1`.
5. Proposal `base_image` digest is **pullable on every host** (local registry, `ghcr.io`, or pre-pushed). Prepare will autokick hosts that cannot pull.
6. Enough free disk (daemon enforces a minimum; raise only if you know the host policy).

Do **not** assemble / prepare during a PoC window if you still need those GPUs for validation — prepare drains inference GPUs. Autokicked nodes **cannot** rejoin the same shard; you must settle and assemble a new one.

---



## 4. Create the TSP (governance proposal)



### 4.1 Pick parameters

```bash
GPU_PROFILE="NVIDIA GEFORCE RTX 3090 | 24GB x1"   # must match opted-in hosts
MAX_NODES=7
MAX_BLOCKS=100000                                 # reservation lifetime in blocks
BASE_IMAGE="127.0.0.1:5055/trainshard-a3@sha256:99fa6660c454cb86db8fb7700b8d540b72e915552655ed0efa6b03a6386e8985"   # pinned digest - replace when needed

# Confirm profile exists / is allowed
./inferenced query inference hardware-nodes-all --node "$NODE" -o json \
  | jq -c '.nodes[].hardware_nodes[].hardware'
./inferenced query inference params --node "$NODE" -o json \
  | jq -r '.params.training_params.allowed_gpu_profile_ids // empty'
```

`MAX_BLOCKS` is how long the **shard reservation** may live once assembled, not the training `STEPS` env (those are independent).

### 4.2 Build and submit the gov proposal

Deposit amount is chain-specific (testnet1: `25000000ngonka`). Gov module account is the `authority`.

```bash
GOV=$(./inferenced query auth module-account gov --node "$NODE" --home "$HOME_INF" -o json \
  | jq -r '.account.value.address // .account.base_account.address')
CREATOR=$(./inferenced keys show "$KEY" -a --keyring-backend file --home "$HOME_INF")

jq -n \
  --arg a "$GOV" --arg c "$CREATOR" --arg p "$GPU_PROFILE" --arg i "$BASE_IMAGE" \
  --argjson n "$MAX_NODES" --argjson b "$MAX_BLOCKS" \
'{
  messages: [{
    "@type": "/inference.inference.MsgCreateTrainshardProposal",
    authority: $a,
    creator: $c,
    gpu_profile_id: $p,
    max_nodes: $n,
    max_duration_blocks: ($b|tostring),
    base_image: $i,
    run_key: ""
  }],
  metadata: "trainshard",
  deposit: "25000000ngonka",
  title: "N-node training run",
  summary: "Lease GPUs for one trainshard run"
}' > tsp-proposal.json

./inferenced tx gov submit-proposal tsp-proposal.json \
  --from "$KEY" --keyring-backend file --home "$HOME_INF" \
  --chain-id "$CHAIN_ID" --node "$NODE" "${GAS[@]}"
```

Note the `txhash`, then resolve the **governance proposal id**:

```bash
TX=<txhash>
# wait until indexed, then:
./inferenced query tx "$TX" --node "$NODE" -o json | jq -r '
  .events[] | select(.type=="submit_proposal") | .attributes[] | select(.key=="proposal_id") | .value'
```

Call that id `GOV_ID`.

### 4.3 Vote yes (enough bonded voting power)

Voting period can be short on testnet (e.g. ~120s). Vote from validators that still have power:

```bash
inferenced tx gov vote "$GOV_ID" yes \
  --from "$KEY" --keyring-backend file --home "$HOME_INF" \
  --chain-id "$CHAIN_ID" --node "$NODE" "${GAS[@]}"

# Repeat from other bonded validators as needed, then:
inferenced query gov proposal "$GOV_ID" --node "$NODE" -o json | jq -r '.proposal.status'
# expect PROPOSAL_STATUS_PASSED
```



### 4.4 Find the trainshard proposal id (TSP)

Gov id ≠ trainshard proposal id. After the gov proposal passes, list open / consumed TSPs:

```bash
# Scan recent TSP ids (adjust range)
for i in $(seq 1 80); do
  ./inferenced query inference show-trainshard-proposal "$i" --node "$NODE" -o json 2>/dev/null \
    | jq -c --argjson i "$i" 'select(.proposal != null) | {
        id: $i,
        status: .proposal.status,
        max_nodes: .proposal.max_nodes,
        max_duration_blocks: .proposal.max_duration_blocks,
        creator: .proposal.creator,
        gpu: .proposal.gpu_profile_id
      }'
done
```

Pick the TSP that matches your creator, profile, `max_nodes`, and `max_duration_blocks`, status **OPEN** (not yet consumed). Call it `TSP_ID`.

---



## 5. Assemble → prepare → deploy → start

Run these on the machine that has `trainshardctl` + the creator keyring (local laptop or coordinator host).

```bash
# 5.1 Reserve nodes from the passed TSP
shard=$(trainshardctl assemble "$TSP_ID")
echo "shard=$shard"

# 5.2 Drain GPUs, pull base image, bring up WireGuard mesh
trainshardctl prepare "$shard" -wait 30m
trainshardctl status "$shard"
# Every intended node: PREPARED true, MESH true. REASON explains failures.
# A node that cannot prepare in time is AUTOKICKED and cannot rejoin this shard.

# 5.3 Place the run (image must be based on the proposal base_image digest)
trainshardctl deploy "$shard" \
  -image "$BASE_IMAGE" \
  -gpus 1 \
  -disk-bytes 10737418240 \
  -env STEPS=100000
# Optional egress: -source host:port (repeatable)
# Optional command after -- :  -- python -u /opt/train/train.py

# 5.4 Start
trainshardctl start "$shard"
trainshardctl status "$shard"
# Expect containers running, MESH true, GPUs claimed
```

`NODE_RANK`, `NNODES`, `MASTER_ADDR`, and `MASTER_PORT` are injected by the daemon. Do not bake a conflicting rank into the image entrypoint without reading those env vars.

---



## 6. Monitor

```bash
trainshardctl status "$shard"

# Logs: participant/node as printed by status
trainshardctl logs "$shard" gonka1.../node1 -tail 50

# On a host (optional):
# docker logs -f trainshard-22-node1
```

Healthy multi-node run: all ranks advance the same step, loss moves, no NCCL/OOM floods. Chain view:

```bash
./inferenced query inference show-trainshard "$shard" --node "$NODE" -o json \
  | jq '{status: .trainshard.status, nodes: [.trainshard.nodes[] | {node_id, status, release_reason}]}'
```

---



## 7. Stop, report, settle

**Training finishing (**`STEPS` **done) does not release GPUs.** The shard stays `ACTIVE` until you settle (or it hits `expires_at_height`).

```bash
# When the job is done (or you want to abort):
trainshardctl stop "$shard" -grace 2m

# Capture artifacts while hosts still answer:
trainshardctl report "$shard"

# Close on chain and return GPUs to inference:
trainshardctl settle "$shard"

inferenced query inference show-trainshard "$shard" --node "$NODE" -o json \
  | jq -r '.trainshard.status'   # TRAINSHARD_STATUS_SETTLED
```


| Action                           | Effect                                      |
| -------------------------------- | ------------------------------------------- |
| Process exits / `STEPS` complete | Containers done; **reservation still held** |
| `stop`                           | Stops containers; shard still open on chain |
| `settle`                         | Chain closes shard; hosts get GPUs back     |
| Early `settle` without finishing | Aborts the run; frees GPUs now              |


Do not stop `trainshardd` on a host while that node is still reserved — wait until the shard is settled.

---



## 8. Common failures


| Symptom                                    | Likely cause                                                       |
| ------------------------------------------ | ------------------------------------------------------------------ |
| Assemble gets fewer nodes than `max_nodes` | Opt-ins expired / wrong profile / daemon down / farmer holding GPU |
| Prepare autokick `failed_prepare`          | Image pull failed, mesh identity, drain, disk gate                 |
| Proxy `405` on trainshard routes           | Proxy image without trainshard routes                              |
| `trainshardctl` cannot dial chain          | Pointed at HTTP `/chain-rpc/` instead of raw `:9090` gRPC          |
| Settle/assemble “wrong creator”            | Different key than TSP `creator`                                   |
| Want 7 nodes after a kick                  | Impossible on same shard — settle and assemble a new TSP           |


---



## Reference: minimal happy path

```bash
# after env + PASSED TSP_ID + BASE_IMAGE are set
shard=$(trainshardctl assemble "$TSP_ID")
trainshardctl prepare "$shard" -wait 30m
trainshardctl deploy "$shard" -image "$BASE_IMAGE" -gpus 1 -disk-bytes 10737418240 -env STEPS=1000
trainshardctl start "$shard"
# ... wait for done ...
trainshardctl stop "$shard" -grace 2m
trainshardctl report "$shard"
trainshardctl settle "$shard"
```

