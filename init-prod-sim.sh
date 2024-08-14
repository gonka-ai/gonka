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

<<'###BLOCK-COMMENT'
echo requester'\n'executor'\n'validator \
    | xargs -I {} \
    docker run --rm -i \
    -v $MOUNT_PATH/{}/node:/root/$STATE_DIR_NAME \
    "$IMAGE_NAME" \
    "$APP_NAME" init \
    --chain-id $CHAIN_ID \
    --default-denom $COIN_DENOM \
    prod-sim-node # moniker is not chain id!
###BLOCK-COMMENT

docker run --rm -it \
    -v "$MOUNT_PATH/requester/node:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=requester IS_GENESIS=true ./init-docker.sh"

function get_node_id() {
  local x=$1
  docker run --rm \
      -v "$MOUNT_PATH/$x/node:/root/$STATE_DIR_NAME" \
      "$IMAGE_NAME" \
      "$APP_NAME" tendermint show-node-id
}

requester_node_id=$(get_node_id requester)
echo "requester_node_id=$requester_node_id"

SEEDS="$requester_node_id@requester:26656"

echo "--INITIALIZING EXECUTOR--"
docker run --rm -it \
    -v "$MOUNT_PATH/executor/node:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=executor SEEDS=$SEEDS ./init-docker.sh"

executor_node_id=$(get_node_id executor)

SEEDS="\"$requester_node_id@requester:26656,$executor_node_id@executor:26656\""

echo "--INITIALIZING EXECUTOR--"
# TODO: add executor as a seed too?
docker run --rm -it \
    -v "$MOUNT_PATH/validator/node:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=validator SEEDS=$SEEDS ./init-docker.sh"

function initialize_config() {
    local x=$1
    local yaml_file="$MOUNT_PATH/$x/api/config.yaml"
    cp decentralized-api/config-docker.yaml "$yaml_file"

    sed -i '' "s|^[[:space:]]url: .*|url: http://$x:26657|" "$yaml_file"
    sed -i '' "s|^[[:space:]]account_name: \".*\"|account_name: \"$x\"|" "$yaml_file"
    sed -i '' "s|^[[:space:]]keyring_backend: \".*\"|keyring_backend: \"file\"|" "$yaml_file"
}

initialize_config requester
cp decentralized-api/config.yaml "$MOUNT_PATH/requester/api/config.yaml"
cp decentralized-api/config.yaml "$MOUNT_PATH/executor/api/config.yaml"
cp decentralized-api/config.yaml "$MOUNT_PATH/validator/api/config.yaml"