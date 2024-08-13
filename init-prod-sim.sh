rm -r prod-sim

mkdir prod-sim
mkdir prod-sim/requester
mkdir prod-sim/requester/node
mkdir prod-sim/requester/api
mkdir prod-sim/executor
mkdir prod-sim/executor/node
mkdir prod-sim/executor/api
mkdir prod-sim/validator
mkdir prod-sim/validator/node
mkdir prod-sim/validator/api

APP_NAME="inferenced"
IMAGE_NAME="inferenced"
CHAIN_ID="test-chain"
COIN_DENOM="icoin"

echo requester'\n'executor'\n'validator \
    | xargs -I {} \
    docker run --rm -i \
    -v $(pwd)/prod-sim/{}/node:/root/.$APP_NAME \
    "$IMAGE_NAME" \
    "$APP_NAME" init \
    --chain-id $CHAIN_ID \
    --default-denom $COIN_DENOM \
    prod-sim-node # moniker is not chain id!
