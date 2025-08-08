if [ -z "$GENESIS_INDEX" ]; then
  echo "GENESIS_INDEX is not set. Please set it to the index of the validator."
  exit 1
fi
export GENESIS_INDEX

if [ -z "$BASE_DIR" ]; then
  echo "BASE_DIR is not set. Please set it to the base directory for test artifacts."
  BASE_DIR="."
else
  echo "Using BASE_DIR: $BASE_DIR"
fi

export KEY_NAME="genesis-$GENESIS_INDEX"
export DATA_MOUNT_PATH="$BASE_DIR/genesis-$GENESIS_INDEX"
export GENESIS_RUN_STAGE="start"

echo "Clearing any previous runs"
docker compose -p "$KEY_NAME" down || true
rm -rf "${DATA_MOUNT_PATH}/node"
rm -rf "${DATA_MOUNT_PATH}/api"

if [ "$GENESIS_INDEX" != "0" ]; then
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
fi

echo "Starting for $GENESIS_RUN_STAGE"
echo "KEY_NAME=$KEY_NAME"
echo "DATA_MOUNT_PATH=$DATA_MOUNT_PATH"
echo "GENESIS_RUN_STAGE=$GENESIS_RUN_STAGE"
echo "GENESIS_INDEX=$GENESIS_INDEX"

docker compose -p "$KEY_NAME" -f docker-compose.yml up -d
