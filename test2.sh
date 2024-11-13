set -e

docker compose -f docker-compose-genesis.yml down
docker compose -f docker-compose-local.yml down -v
docker compose -p join1 down
docker compose -p join2 down

make node-build-genesis
make api-build-docker
make api-release-docker

PORT=8080
KEY_NAME=genesis
NODE_CONFIG=node_payload.json
BASE_DIR="prod-genesis"
rm -r "$BASE_DIR" || true

docker compose -f docker-compose-genesis.yml up -d
sleep 20

# Add inference nodes
curl -X POST "http://localhost:$PORT/v1/nodes/batch" -H "Content-Type: application/json" -d @$NODE_CONFIG

echo "Adding self as participant"
# Run the docker exec command and capture the validator_output
validator_output=$(docker exec "$KEY_NAME-node" inferenced tendermint show-validator)

# Use jq to parse the JSON and extract the "key" value
validator_key=$(echo $validator_output | jq -r '.key')

echo "validator_key=$validator_key"

unique_models=$(jq '[.[] | .models[]] | unique' $NODE_CONFIG)

# Prepare the data structure for the final POST
post_data=$(jq -n \
  --arg url "http://$KEY_NAME-api:8080" \
  --argjson models "$unique_models" \
  --arg validator_key "$validator_key" \
  '{
    url: $url,
    models: $models,
    validator_key: $validator_key,
  }')

echo "POST request sent to $ADD_ENDPOINT with the following data:"
echo "$post_data"

# Make the final POST request to the genesis endpoint
curl -X POST "http://0.0.0.0:8080/v1/participants" -H "Content-Type: application/json" -d "$post_data"


sleep 10
genesis_node_id=$(docker exec genesis-node inferenced tendermint show-node-id)
echo "genesis_node_id=$genesis_node_id"
cp $BASE_DIR/config/genesis.json ./inference-chain/build/genesis.json
echo "Genesis file copied"
export SEEDS="$genesis_node_id@genesis-node:26656"
echo SEEDS="$genesis_node_id@genesis-node:26656" > ./inference-chain/build/config.env
echo "Genesis node id added to config.env"
echo "ADD_ENDPOINT=\"http://genesis-node:$PORT\"" >> ./inference-chain/build/config.env
echo "Genesis node endpoint added to config.env"

make node-release-docker
make node-build-docker
#
export KEY_NAME=join1
export NODE_CONFIG=$NODE_CONFIG
export ADD_ENDPOINT="http://0.0.0.0:$PORT"
export PUBLIC_URL="http://join1-api:8080"
export PORT=8081
./launch_chain.sh
export KEY_NAME=join2
export PORT=8082
export PUBLIC_URL="http://join2-api:8080"
./launch_chain.sh

curl -X POST "https://maker.ifttt.com/trigger/pushover_alert/with/key/bSVa981BFD2BtZZhn3DnTe?value1=TestRead&value2=Inference-ignite"