# Download latest inferenced binary to create a dev account
# Developer instruction:
#  https://testnet.productscience.ai/developer/quickstart/
export ACCOUNT_NAME="test-account"
# Url of the genesis k8s node API
export NODE_URL="http://34.9.136.116:30000/api"
# export NODE_URL="http://34.9.136.116:30010"
export GONKA_ENDPOINTS=$NODE_URL/v1
# export INFERENCED_BINARY="kubectl -n genesis exec node-0 -- inferenced"
# export INFERENCED_BINARY="inferenced"
export INFERENCED_BINARY="/Users/dima/cosmos/bin/inferenced"

# Example endpoints to check the server status with
curl "$NODE_URL/v1/status" | jq
curl "$NODE_URL/v1/epochs/current/participants" | jq

# Create account, should return 200
"$INFERENCED_BINARY" create-client $ACCOUNT_NAME \
  --node-address "$NODE_URL"

export GONKA_ADDRESS="gonka1vsdg4uxa74tuskzdy764xjxt64tea8lv5prut5"

# View it
"$INFERENCED_BINARY" keys list

"$INFERENCED_BINARY" query bank balances "$GONKA_ADDRESS" \
  --node tcp://34.9.136.116:30000/chain-rpc/ # trailing slash in necesary for now

# Export private key:
GONKA_PRIVATE_KEY="$(echo y | "$INFERENCED_BINARY" keys export $ACCOUNT_NAME --unarmored-hex --unsafe)"
echo "$GONKA_PRIVATE_KEY"

# Use compressa:
# Prerequisite, create and activate venv:
# 1. python3 -m venv compressa-venv
# 2. source compressa-venv/bin/activate
# 3. pip install git+https://github.com/product-science/compressa-perf.git
# 4. Download: https://github.com/product-science/inference-ignite/blob/main/mlnode/packages/benchmarks/resources/config.yml
#    No need to change anything inside the config.yml file.

export GONKA_ADDRESS
./check-balances.sh

compressa-perf measure-from-yaml \
  --private_key_hex "$GONKA_PRIVATE_KEY" \
  --account_address "$GONKA_ADDRESS" \
  --node_url $NODE_URL \
  config.yml \
  --model_name Qwen/Qwen2.5-7B-Instruct

compressa-perf measure-from-yaml \
  --private_key_hex "$GONKA_PRIVATE_KEY" \
  --account_address "$GONKA_ADDRESS" \
  --node_url $NODE_URL \
  config-2.yml \
  --model_name Qwen/Qwen2.5-1.5B-Instruct

export GONKA_ADDRESS
./check-balances.sh

kubectl -n genesis exec node-0 -- inferenced query inference list-inference --output json

kubectl -n genesis exec node-0 -- inferenced query inference params --output json

kubectl -n genesis exec node-0 -- inferenced query bank balances gonka1mfyq5pe9z7eqtcx3mtysrh0g5a07969zxm6pfl --output json

# Tunnel to admin API, might be useful to check node status
kubectl port-forward -n genesis svc/api-private 9200:9200
