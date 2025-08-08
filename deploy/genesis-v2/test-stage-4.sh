#!/bin/bash
set -e
set -x

source test-utils.sh
export HOST_ACCESS_ADDR=${HOST_ACCESS_ADDR:-127.0.0.1}
export BASE_DIR="./multigen-tests"

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

rm -rf "$BASE_DIR/genesis-0/input-artifacts/gentx"
mkdir -p "$BASE_DIR/genesis-0/input-artifacts/gentx"

# Copy all gentx to genesis-0
for i in $(seq 0 $(($NUM_VALIDATORS - 1))); do
  # Copy all
  cp "$BASE_DIR/genesis-$i/node/config/gentx"/*.json "$BASE_DIR/genesis-0/input-artifacts/gentx/."

  # Tear down any existing containers
  docker compose -p "genesis-$i" down
done

init_ports "0"
export GENESIS_INDEX="0"
export P2P_EXTERNAL_ADDRESS="$HOST_ACCESS_ADDR:$((26656 + 0 * 10))"
./stage-4-start.sh

sleep 30

for i in $(seq 1 $(($NUM_VALIDATORS - 1))); do
  cp "$BASE_DIR/genesis-0/artifacts/genesis-final.json" "$BASE_DIR/genesis-$i/input-artifacts/genesis-final.json"

  init_ports "$i"
  export P2P_EXTERNAL_ADDRESS="$HOST_ACCESS_ADDR:$((26656 + $i * 10))"
  export GENESIS_INDEX="$i"

  # Compose persistent peers pointing to genesis-0 only for simplicity
  PEERS_DIR="$BASE_DIR/genesis-0/artifacts"
  NODE_ID_FILE="$PEERS_DIR/node_id.txt"

  if [ -f "$NODE_ID_FILE" ]; then
    # default local addressing via host.docker.internal and derived port
    GEN0_P2P_PORT=$((26656 + 0 * 10))
    export CONFIG_p2p__persistent_peers="$(cat "$NODE_ID_FILE")@host.docker.internal:${GEN0_P2P_PORT}"
    echo "Setting persistent_peers to: $CONFIG_p2p__persistent_peers"
  else
    echo "Warning: $NODE_ID_FILE not found; starting without persistent peers."
    unset CONFIG_p2p__persistent_peers
  fi

  # Provide RPC endpoint of genesis-0 for fetching genesis.json inside containers
  GEN0_RPC_PORT=$((26657 + 0 * 10))
  : "${HOST_ACCESS_ADDR:?HOST_ACCESS_ADDR must be set to the host IP/DNS reachable from containers}"
  export GENESIS0_RPC_HOST="$HOST_ACCESS_ADDR"
  export GENESIS0_RPC_PORT="$GEN0_RPC_PORT"
  echo "Propagating GENESIS0_RPC_HOST=$GENESIS0_RPC_HOST GENESIS0_RPC_PORT=$GENESIS0_RPC_PORT"

  ./stage-4-start.sh
done
