if [ -z "$GENESIS_INDEX" ]; then
  echo "GENESIS_INDEX is not set. Please set it to the index of the validator."
  exit 1
fi

source test-utils.sh

echo "Running stage-1-keygen.sh for genesis-$GENESIS_INDEX"

export KEY_NAME="genesis-$GENESIS_INDEX"
export DATA_MOUNT_PATH="./$BASE_DIR/genesis-$GENESIS_INDEX"
export GENESIS_RUN_STAGE="keygen"

echo "Starting keygen"
echo "KEY_NAME=$KEY_NAME"
echo "DATA_MOUNT_PATH=$DATA_MOUNT_PATH"
echo "GENESIS_RUN_STAGE=$GENESIS_RUN_STAGE"

docker compose -p "$KEY_NAME" -f docker-compose.yml up tmkms node -d
