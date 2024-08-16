# Not a script. Just a sequence of steps I did to deploy the testnet

# 1. Log into requester
gssh requester-node
gcloud auth configure-docker

APP_NAME="inferenced"
IMAGE_NAME="gcr.io/decentralized-ai/inferenced"
COIN_DENOM="icoin"
STATE_DIR_NAME=".inference"
MOUNT_PATH=$(pwd)

docker run --rm -it \
    -v "$MOUNT_PATH/$STATE_DIR_NAME:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=requester IS_GENESIS=true ./init-docker.sh"

docker run --rm \
    -v "$MOUNT_PATH/$STATE_DIR_NAME:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    "$APP_NAME" tendermint show-node-id

REQUESTER_NODE_ID="d519dee3b9f6acf3b1d1b95f830145ee3f1f43d5"

# 2. Log into executor
gssh executor-node
gcloud auth configure-docker

APP_NAME="inferenced"
IMAGE_NAME="gcr.io/decentralized-ai/inferenced"
COIN_DENOM="icoin"
STATE_DIR_NAME=".inference"
MOUNT_PATH=$(pwd)

SEEDS="d519dee3b9f6acf3b1d1b95f830145ee3f1f43d5@10.128.0.21:26656"

docker run --rm -it \
    -v "$MOUNT_PATH/executor:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=executor SEEDS=$SEEDS ./init-docker.sh"

docker run --rm \
    -v "$MOUNT_PATH/$STATE_DIR_NAME:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    "$APP_NAME" tendermint show-node-id

gscp docker-compose-cloud.yml requester-node:~/docker-compose-cloud.yml
