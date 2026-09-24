# Running a trainshard

How to lease GPUs and train on them: what each machine sets, what the
coordinator puts on chain, and how a run is driven and given back. The example
is a small GPT trained across the shard, one card to a node, from
[`trainshard/example/train.py`](../trainshard/example/train.py).

## On each machine

1. Fill the trainshard block in `config.env` (it is in
   [`deploy/join/config.env.template`](../deploy/join/config.env.template),
   commented out):

```
export TRAINSHARD_SERVICE_NAME=trainshardd
export TRAINSHARD_PARTICIPANT=gonka1...          # your address
export TRAINSHARD_NODES=node1                    # the node this machine's mlnode is registered as
export TRAINSHARD_ENDPOINT=http://host1.example.com:8000   # where a coordinator reaches you: the proxy
export TRAINSHARD_MESH_ENDPOINT=203.0.113.10     # public address peers reach you at, not behind nat
export TRAINSHARD_MESH_PORTS=51820-51827         # one per leased node, udp, open on the host to the internet
export TRAINSHARD_STATE_DIR=/mnt/xfs/trainshardd # xfs with prjquota
export TRAINSHARD_CONTAINER_MEMORY_BYTES=137438953472
export TRAINSHARD_CONTAINER_NANO_CPUS=8000000000
```

The daemon signs with the key the api uses (`KEY_NAME` from `.inference`). If
that is a warm key rather than the participant's own, it needs the ML ops
grants from the participant, the same ones the api runs on:

```
inferenced tx inference grant-ml-ops-permissions <account-key> <warm-address> --from <account-key> --gas auto --gas-adjustment 1.5 --yes
```

2. Start the daemon, the same compose command as always plus one file:

```
docker compose -f docker-compose.yml -f docker-compose.trainshard.yml up -d
```

3. Check the node is ready. The daemon runs its checks every 5 min (GPUs match
   what the api put on chain, key granted, disk, mesh port, version) and only
   refreshes the opt-in when all pass; a failed check is logged with its reason:

```
docker logs --tail 20 trainshardd
inferenced query txs --query "message.action='/inference.inference.MsgRefreshTrainingNodeOptIn'" -o json | jq -r '.txs[-1].height'
```

Once a node is reserved the same log says what it is still waiting on, one
line per change, and `node prepared` when it is ready:

```
INFO node not prepared node_id=node1 waiting_for="node not drained from inference, base image not pulled"
INFO node not prepared node_id=node1 waiting_for="no mesh identity"
INFO node prepared node_id=node1
```

A node that waits on the same thing for longer than the daemon's patience
(`TRAINSHARD_PREPARE_DEADLINE`, default 30m) is handed back to the chain.

4. To stop leasing, stop the daemon. It is what keeps the node opted in, so an
   opt-out sent while it runs is undone at its next refresh. Once it is stopped the
   opt-in lapses on its own after `training_params.opt_in_ttl_blocks`; starting it
   again opts the node back in. Do not stop it while the node is reserved: wait
   until the shard is settled.

```
docker compose -f docker-compose.yml -f docker-compose.trainshard.yml stop trainshardd
```

### More than one GPU machine

One daemon per GPU machine, next to that machine's mlnode. On the machine that
runs the api and the chain node, publish both to the GPU machines on a private
address (a VPN one: the api's admin port signs any message with your key for
whoever reaches it) and route each GPU machine through the proxy under its own
prefix:

```
export TRAINSHARD_HUB_BIND=10.0.0.11                                  # private, reachable from the gpu machines only
export TRAINSHARD_ROUTES="node2=10.0.0.12:9700 node3=10.0.0.13:9700"  # name=daemon address, one per machine
docker compose -f docker-compose.yml -f docker-compose.trainshard.yml -f docker-compose.trainshard-hub.yml up -d
```

Leave `docker-compose.trainshard.yml` out when this machine leases no GPUs of
its own. On each GPU machine, with a warm key of its own in `.inference`:

```
export TRAINSHARD_NODES=node2
export TRAINSHARD_ENDPOINT=http://host1.example.com:8000/trainshard-node2   # its own prefix, never the same as another machine's
export TRAINSHARD_BIND=10.0.0.12                 # where the first machine's proxy reaches this daemon
export TRAINSHARD_MESH_ENDPOINT=203.0.113.12
export TRAINSHARD_CHAIN_GRPC=10.0.0.11:9090
export TRAINSHARD_DAPI=http://10.0.0.11:9200
docker compose -f docker-compose.mlnode.yml -f docker-compose.trainshard.yml up -d
```

## On the coordinator

1. Take the GPU profile string from the hardware the hosts report, it is
   `<TYPE> x<count>` per node with the type upper-cased, such as
   `TESLA T4 | 15GB x1`:

```
inferenced query inference hardware-nodes-all -o json | jq -c '.nodes[].hardware_nodes[].hardware'
```

The api fills the hardware in from the mlnode's GPU endpoint as `<name> | <GB>GB`
per card, and the daemon checks the same string against `nvidia-smi` before it
opts a node in. A node whose hardware is declared by hand in `node-config.json`
has to spell it the same way, `{"type":"Tesla T4 | 15GB","count":1}`, or the
`gpus_match_chain` check keeps it out of the pool.

Any profile is accepted unless governance filled
`training_params.allowed_gpu_profile_ids`, then yours has to be in that list:

```
inferenced query inference params -o json | jq -r '.params.training_params.allowed_gpu_profile_ids[]'
```

2. Build the run image, the digest is what the proposal pins. `train.py` reads
   `NODE_RANK`, `NNODES`, `MASTER_ADDR` and `MASTER_PORT` from the environment,
   that is the whole contract. Bake it anywhere but `/workspace`: that path is
   the volume the daemon mounts, and it hides whatever the image left there:

```
cp trainshard/example/train.py .
curl -sL https://raw.githubusercontent.com/karpathy/char-rnn/master/data/tinyshakespeare/input.txt -o input.txt

cat > Dockerfile <<'EOF'
FROM pytorch/pytorch@sha256:8312479...
COPY train.py input.txt /opt/train/
ENTRYPOINT ["python","-u","/opt/train/train.py"]
EOF

docker build -t myrepo/trainer:1 . && docker push myrepo/trainer:1
docker inspect myrepo/trainer:1 --format '{{index .RepoDigests 0}}'
```

3. Create the run and vote it through, only the four values on top change
   between runs:

```
GPU_PROFILE="TESLA T4 | 15GB x1"; MAX_NODES=2; MAX_BLOCKS=500; BASE_IMAGE=myrepo/trainer@sha256:...

jq -n --arg a "$(inferenced query auth module-account gov -o json | jq -r .account.value.address)" \
      --arg c "$(inferenced keys show <key> -a)" --arg p "$GPU_PROFILE" --arg i "$BASE_IMAGE" \
      --argjson n $MAX_NODES --argjson b $MAX_BLOCKS '{
  messages: [{"@type":"/inference.inference.MsgCreateTrainshardProposal", authority:$a, creator:$c,
    gpu_profile_id:$p, max_nodes:$n, max_duration_blocks:$b, base_image:$i, run_key:""}],
  metadata:"trainshard", deposit:"1000000ngonka", title:"a training run", summary:"lend gpus for one run"
}' > run.json

inferenced tx gov submit-proposal run.json --from <key> --gas auto --gas-adjustment 1.5 --yes
inferenced tx gov vote $(inferenced query gov proposals -o json | jq -r '.proposals[-1].id') yes \
  --from <key> --gas auto --gas-adjustment 1.5 --yes
```

4. Point trainshardctl at the chain; the hosts' addresses come from the shard record:

```
export TRAINSHARD_CHAIN_GRPC=chain-host:9090
export TRAINSHARD_CHAIN_ID=gonka-mainnet    # optional, read from the chain and only checked when set
export TRAINSHARD_KEY_NAME=mykey
export TRAINSHARD_KEYRING_DIR=$HOME/.inference
export TRAINSHARD_KEYRING_BACKEND=test      # default: file
```

## Running the training

1. Reserve the nodes and bring up the mesh:

```
shard=$(trainshardctl assemble <trainshard-proposal-id>)
trainshardctl prepare $shard --wait 5m        # default: 30m
trainshardctl status $shard                   # PREPARED true; REASON says what a false one waits on
```

2. Place and start the run:

```
trainshardctl deploy $shard --image myrepo/trainer@sha256:... --gpus 1 --disk-bytes 2147483648 \
  --env STEPS=60
trainshardctl start $shard
trainshardctl status $shard                   # every node running, MESH true
```

`NODE_RANK`, `NNODES`, `MASTER_ADDR` and `MASTER_PORT` are handed to the
container by the daemon. The run has no route out, `--source host:port` is the
only way to open one.

3. Read it:

```
trainshardctl logs $shard gonka1host1.../node1 --tail 30    # default: 0, whole log
```

It passed when the loss falls, every node reaches done, and the checksums
match, which only happens if gradients crossed the mesh:

```
rank 0 done: loss 2.5098, 4354ms/step, weight checksum 19789.083002
rank 1 done: loss 2.4709, 4354ms/step, weight checksum 19789.083002
```

4. Give the nodes back:

```
trainshardctl stop $shard --grace 30s         # default: 30s
trainshardctl settle $shard
inferenced query inference show-trainshard $shard -o json | jq -r '.trainshard.status'   # SETTLED
```
