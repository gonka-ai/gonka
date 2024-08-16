BASE_DIR="gcp-prod-sim"
rm -r "$BASE_DIR"

mkdir "$BASE_DIR"
mkdir "$BASE_DIR/requester"
mkdir "$BASE_DIR/executor"
mkdir "$BASE_DIR/validator"

APP_NAME="inferenced"
IMAGE_NAME="gcr.io/decentralized-ai/inferenced"
COIN_DENOM="icoin"
STATE_DIR_NAME=".inference"

MOUNT_PATH=$(pwd)/$BASE_DIR
echo "MOUNT_PATH=$MOUNT_PATH"

docker run --rm -it \
    -v "$MOUNT_PATH/requester:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=requester IS_GENESIS=true ./init-docker.sh"

function get_node_id() {
  local x=$1
  docker run --rm \
      -v "$MOUNT_PATH/$x:/root/$STATE_DIR_NAME" \
      "$IMAGE_NAME" \
      "$APP_NAME" tendermint show-node-id
}

requester_node_id=$(get_node_id requester)
echo "requester_node_id=$requester_node_id"

SEEDS="$requester_node_id@10.128.0.21:26656"

echo "--INITIALIZING EXECUTOR--"
docker run --rm -it \
    -v "$MOUNT_PATH/executor:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=executor SEEDS=$SEEDS ./init-docker.sh"

executor_node_id=$(get_node_id executor)

SEEDS="\"$requester_node_id@10.128.0.21:26656,$executor_node_id@10.128.0.22:26656\""

echo "--INITIALIZING EXECUTOR--"
docker run --rm -it \
    -v "$MOUNT_PATH/validator:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=validator SEEDS=$SEEDS ./init-docker.sh"

# Distribute the genesis
cp "$MOUNT_PATH/requester/config/genesis.json" "$MOUNT_PATH/executor/config/genesis.json"
cp "$MOUNT_PATH/requester/config/genesis.json" "$MOUNT_PATH/validator/config/genesis.json"

function initialize_config() {
    local x=$1
    local yaml_file="$MOUNT_PATH/$x/api-config.yaml"
    cp decentralized-api/config-docker.yaml "$yaml_file"

    sed -i '' "s/account_name: .*/account_name: \"$x\"/" "$yaml_file"
}

initialize_config requester
initialize_config executor
initialize_config validator

v_addr="cosmos1x4c24exedfdy6czz5ck92ka9xw2pdd2eq76gh3"
e_addr="cosmos1xql6r5dqqljs4j0me8s6ummadyvpwjwuga06sp"
r_addr="cosmos1yq6duhnjl6jr0dwxsjmv9ujfjscxzp2u6v6cw9"
