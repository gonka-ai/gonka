BASE_DIR="prod-sim"
rm -r "$BASE_DIR"

mkdir "$BASE_DIR"
mkdir "$BASE_DIR/requester"
mkdir "$BASE_DIR/requester/node"
mkdir "$BASE_DIR/requester/api"
mkdir "$BASE_DIR/executor"
mkdir "$BASE_DIR/executor/node"
mkdir "$BASE_DIR/executor/api"
mkdir "$BASE_DIR/validator"
mkdir "$BASE_DIR/validator/node"
mkdir "$BASE_DIR/validator/api"

APP_NAME="inferenced"
IMAGE_NAME="inferenced"
CHAIN_ID="test-chain"
COIN_DENOM="icoin"
STATE_DIR_NAME=".inference"

MOUNT_PATH=$(pwd)/prod-sim
echo "MOUNT_PATH=$MOUNT_PATH"

echo requester'\n'executor'\n'validator \
    | xargs -I {} \
    docker run --rm -i \
    -v $MOUNT_PATH/{}/node:/root/$STATE_DIR_NAME \
    "$IMAGE_NAME" \
    "$APP_NAME" init \
    --chain-id $CHAIN_ID \
    --default-denom $COIN_DENOM \
    prod-sim-node # moniker is not chain id!
