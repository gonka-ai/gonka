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
export TRAINSHARD_NODES=node1                    # nodes to lease, comma separated
export TRAINSHARD_MESH_ENDPOINT=203.0.113.10     # address peers reach you at
export TRAINSHARD_MESH_PORTS=51820-51827         # one per leased node
export TRAINSHARD_STATE_DIR=/mnt/xfs/trainshardd # xfs with prjquota
export TRAINSHARD_GPUS=8
export TRAINSHARD_GPU_MODEL=H100
export TRAINSHARD_CONTAINER_MEMORY_BYTES=137438953472
export TRAINSHARD_CONTAINER_NANO_CPUS=8000000000
```

2. Start the daemon, the same compose command as always plus one file:

```
docker compose -f docker-compose.yml -f docker-compose.trainshard.yml up -d
```

3. Check the opt-in landed on chain (the daemon refreshes it every 5 min):

```
docker logs --tail 20 trainshardd
inferenced query txs --query "message.action='/inference.inference.MsgRefreshTrainingNodeOptIn'" -o json | jq -r '.txs[-1].height'
```

## On the coordinator

1. Take the GPU profile string from the hardware the hosts report, it is
   `<TYPE> x<count>` per node, such as `TESLA T4 x1`:

```
inferenced query inference hardware-nodes-all -o json | jq -c '.nodes[].hardware_nodes[].hardware'
```

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
GPU_PROFILE="TESLA T4 x1"; MAX_NODES=2; MAX_BLOCKS=500; BASE_IMAGE=myrepo/trainer@sha256:...

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

4. Point trainshardctl at the hosts and the chain:

```
echo '{"gonka1host1...":"http://host1.example.com:9700",
       "gonka1host2...":"http://host2.example.com:9700"}' > hosts.json

export TRAINSHARD_HOSTS=$PWD/hosts.json
export TRAINSHARD_CHAIN_GRPC=chain-host:9090
export TRAINSHARD_CHAIN_ID=gonka-mainnet    # default: prod-sim
export TRAINSHARD_KEY_NAME=mykey
export TRAINSHARD_KEYRING_DIR=$HOME/.inference
export TRAINSHARD_KEYRING_BACKEND=test      # default: file
```

## Running the training

1. Reserve the nodes and bring up the mesh:

```
shard=$(trainshardctl assemble <trainshard-proposal-id>)
trainshardctl prepare $shard --wait 5m        # default: 30m
```

2. Place and start the run:

```
trainshardctl deploy $shard --image myrepo/trainer@sha256:... --gpus 1 --disk-bytes 2147483648 \
  --env STEPS=60 --env NCCL_SOCKET_IFNAME=ts0
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
