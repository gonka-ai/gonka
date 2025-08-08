if [ -z "$GENESIS_INDEX" ]; then
  echo "GENESIS_INDEX is not set. Please set it to the index of the validator."
  exit 1
fi

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

echo "Starting for $GENESIS_RUN_STAGE"
echo "KEY_NAME=$KEY_NAME"
echo "DATA_MOUNT_PATH=$DATA_MOUNT_PATH"
echo "GENESIS_RUN_STAGE=$GENESIS_RUN_STAGE"

docker compose -p "$KEY_NAME" -f docker-compose.yml up -d
