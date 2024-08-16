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

sudo chmod o+rw .inference
sudo chmod o+rw .inference/config/genesis.json

docker run --rm \
    -v "$MOUNT_PATH/$STATE_DIR_NAME:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    "$APP_NAME" tendermint show-node-id

REQUESTER_NODE_ID="d519dee3b9f6acf3b1d1b95f830145ee3f1f43d5"

REQUESTER_ADDRESS="cosmos17mj62y074zzl5gjwmuaxgtxy9adqr2rkklveyr"

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
    -v "$MOUNT_PATH/$STATE_DIR_NAME:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=executor SEEDS=$SEEDS ./init-docker.sh"

sudo chmod o+rw .inference
sudo chmod o+rw .inference/config/genesis.json

EXECUTOR_ADDRESS="cosmos1wlnjqegc5k05ulruyex5j58c6g9mfrzj25sfh4"

# 3. Log into validator
gssh validator-node
gcloud auth configure-docker

APP_NAME="inferenced"
IMAGE_NAME="gcr.io/decentralized-ai/inferenced"
COIN_DENOM="icoin"
STATE_DIR_NAME=".inference"
MOUNT_PATH=$(pwd)

SEEDS="d519dee3b9f6acf3b1d1b95f830145ee3f1f43d5@10.128.0.21:26656"

docker run --rm -it \
    -v "$MOUNT_PATH/$STATE_DIR_NAME:/root/$STATE_DIR_NAME" \
    "$IMAGE_NAME" \
    sh -c "chmod +x init-docker.sh; KEY_NAME=validator SEEDS=$SEEDS ./init-docker.sh"

sudo chmod o+rw .inference
sudo chmod o+rw .inference/config/genesis.json
#
VALIDATOR_ADDRESS="cosmos1mdm3dc3xjqqrwuzqk3np6nnzs75zl6j89sasfd"

gscp docker-compose-cloud.yml requester-node:~/docker-compose-cloud.yml
gscp docker-compose-cloud.yml executor-node:~/docker-compose-cloud.yml
gscp docker-compose-cloud.yml validator-node:~/docker-compose-cloud.yml

# Copy genesis.json
gscp requester-node:~/.inference/config/genesis.json genesis.json
gscp genesis.json executor-node:~/.inference/config/genesis.json
gscp genesis.json validator-node:~/.inference/config/genesis.json

# Option 2. And then ssh and mv
gscp requester-node:~/.inference/config/genesis.json genesis.json
gscp genesis.json executor-node:~/genesis.json
gscp genesis.json validator-node:~/genesis.json

gscp executor-node:~/genesis.json e-genesis.json

# Copy api-configs
gscp gcp/requester-config.yaml requester-node:~/.inference/api-config.yaml
gscp gcp/executor-config.yaml executor-node:~/.inference/api-config.yaml
gscp gcp/validator-config.yaml validator-node:~/.inference/api-config.yaml

# Option 2. And then ssh and mv: sudo mv api-config.yaml .inference/
gscp gcp/requester-config.yaml requester-node:~/api-config.yaml
gscp gcp/executor-config.yaml executor-node:~/api-config.yaml
gscp gcp/validator-config.yaml validator-node:~/api-config.yaml

docker compose -f docker-compose-cloud.yml up -d
docker compose -f docker-compose-cloud.yml logs -f
docker compose -f docker-compose-cloud.yml down
