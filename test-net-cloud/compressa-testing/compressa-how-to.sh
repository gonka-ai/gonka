# Download latest inferenced binary to create a dev account
# Developer instruction:
#  https://testnet.productscience.ai/developer/quickstart/
export ACCOUNT_NAME="test-account"
# Url of the genesis k8s node API
export PLAIN_NODE_URL="http://34.9.136.116:30000/"
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

export GONKA_ADDRESS="gonka1q2ghjmr08j0udgv03umq9vl5q38fp3hnhfz8ma"

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
kubectl port-forward -n genesis svc/api 9200:9200

kubectl port-forward -n join-k8s-worker-2 svc/api 9200:9200

# len of prompt in symbols: 3000
# tasks to be executed: 200
# total parallel workers: 100
compressa-perf \
	measure \
	--node_url "$PLAIN_NODE_URL" \
	--model_name Qwen/Qwen2.5-7B-Instruct \
	--create-account-testnet \
	--inferenced-path "$INFERENCED_BINARY" \
	--experiment_name test \
	--generate_prompts \
	--num_prompts 3000 \
	--prompt_length 3000 \
	--num_tasks 200 \
	--num_runners 100 \
	--max_tokens 100

# A continuous test, won't stop until you stop it manually
compressa-perf \
  stress \
  --node_url "$PLAIN_NODE_URL" \
  --model_name Qwen/Qwen2.5-7B-Instruct \
  --create-account-testnet \
  --inferenced-path "$INFERENCED_BINARY" \
  --experiment_name "stress_test" \
  --generate_prompts \
  --num_prompts 2000 \
  --prompt_length 10000 \
  --num_runners 10 \
  --max_tokens 300 \
  --report_freq_min 1 \
  --account-pool-size 10
