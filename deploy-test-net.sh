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

# WAY #2:
# Launch init-prod-sim-2.sh
# Then copy-to-gcp.sh

# Fund accounts
APP_NAME="inferenced"
IMAGE_NAME="gcr.io/decentralized-ai/inferenced"
CHAIN_ID="prod-sim"
COIN_DENOM="icoin"
STATE_DIR_NAME=".inference"

VALIDATOR_ADDRESS="cosmos1zx7sj79mlqe979sawe0jjm9tcady03jgwa3zkc"
EXECUTOR_ADDRESS="cosmos1mrhpzuvp0zf0qgkdg8x8wktgejnrv2g9aja89q"
REQUESTER_ADDRESS="cosmos1445galatfyketym8fjm0wv299zntpstw2s5yud"

echo "Add Executor"
docker run --rm -it \
    -v ~/.inference:/root/.inference \
    "$IMAGE_NAME" \
    "$APP_NAME" tx bank send $REQUESTER_ADDRESS $EXECUTOR_ADDRESS "2500000000$COIN_DENOM" \
        --keyring-backend test --keyring-dir /root/$STATE_DIR_NAME \
        --chain-id $CHAIN_ID --yes \
        --node tcp://10.128.0.21:26657

echo "Add Validator"
docker run --rm -it \
    -v ~/.inference:/root/.inference \
    "$IMAGE_NAME" \
    "$APP_NAME" tx bank send $REQUESTER_ADDRESS $VALIDATOR_ADDRESS "2500000000$COIN_DENOM" \
        --keyring-backend test --keyring-dir /root/$STATE_DIR_NAME \
        --chain-id $CHAIN_ID --yes \
        --node tcp://10.128.0.21:26657

# Get validator keys
docker run --rm \
  -v ~/.inference:/root/.inference \
  "$IMAGE_NAME" \
  "$APP_NAME" tendermint show-validator

# Create participants
# Requester
curl -X POST 'http://34.46.180.72:8080/v1/participants' \
--header 'Content-Type: application/json' \
--data '{
  "url": "http://34.46.180.72:8080",
  "models": ["unsloth/llama-3-8b-Instruct"],
  "validator_key": "hjZiJ3ehIh25KXKZaGnLolgFs3Lw7gxWRceVV24KTMI="
}'

# Executor
curl -X POST 'http://35.232.251.227:8080/v1/participants' \
--header 'Content-Type: application/json' \
--data '{
  "url": "http://35.232.251.227:8080",
  "models": ["unsloth/llama-3-8b-Instruct"],
  "validator_key": "K3L6LZSMTKJZPAlp7L7BESPlkRpB7SPBB5qevDUasK0="
}'

# Validator
curl -X POST 'http://34.172.126.50:8080/v1/participants' \
--header 'Content-Type: application/json' \
--data '{
  "url": "http://34.172.126.50:8080",
  "models": ["unsloth/llama-3-8b-Instruct"],
  "validator_key": "CiAwsLlv9XGQWwi04rD4DyEtzYF2Jiyvj+veWai9hhw="
}'

docker compose -f docker-compose-cloud.yml down
docker rmi gcr.io/decentralized-ai/inferenced gcr.io/decentralized-ai/api

docker compose -f docker-compose-cloud.yml up -d
docker logs --follow api

docker compose -f docker-compose-cloud.yml logs -f
docker logs --follow api

# Check balances
inferenced query bank balances $EXECUTOR_ADDRESS \
  --node tcp://34.46.180.72:26657

inferenced query inference show-inference edbe9960-04b8-4a89-8068-6b271fbcadac \
  --node tcp://34.46.180.72:26657
