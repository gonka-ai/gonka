if [ -z "$GENESIS_INDEX" ]; then
  echo "GENESIS_INDEX is not set. Please set it to the index of the validator."
  exit 1
fi

export KEY_NAME="genesis-$i"
export DATA_MOUNT_PATH="./$BASE_DIR/node$i"
export GENESIS_RUN_STAGE="keygen"

docker-compose -p "$KEY_NAME" -f docker-compose.multigen.yml up tmkms node -d
