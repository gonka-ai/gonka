set -e

mode="$1"
if [ -z "$mode" ]; then
  mode="local"
fi

if [ "$mode" == "local" ]; then
  compose_file="docker-compose-local.yml"
elif [ "$mode" == "cloud" ]; then
  compose_file="docker-compose-cloud-join.yml"
else
  echo "Unknown mode: $mode"
  exit 1
fi

# Verify parameters:
# KEY_NAME - name of the key pair to use
# NODE_CONFIG - name of a file with inference node configuration
# SEED_IP - the ip of the seed node
# PORT - the port to use for the API
# PUBLIC_URL - the access point for getting to your API node from the public

# Much easier to manage the environment variables in a file
# Check if /config.env exists, then source it
if [ -f config.env ]; then
  echo "Souring config.env file..."
  source config.env
fi

if [ -z "$KEY_NAME" ]; then
  echo "KEY_NAME is not set"
  exit 1
fi

if [ -z "$NODE_CONFIG" ]; then
  echo "NODE_CONFIG is not set"
  exit 1
fi

if [ -z "$SEED_IP" ]; then
  echo "SEED_IP is not set"
  exit 1
fi

if [ -z "$PORT" ]; then
  echo "PORT is not set"
  exit 1
fi

if [ -z "$PUBLIC_URL" ]; then
  echo "PUBLIC_URL is not set"
  exit 1
fi

GENESIS_URL="http://$SEED_IP:26657/genesis"
export GENESIS_FILE="genesis.json"

echo "Downloading the genesis file from $GENESIS_URL to $GENESIS_FILE"
wget -q -O - "$GENESIS_URL" | jq -r '.result.genesis' > "$GENESIS_FILE"

SEED_STATUS_URL="http://$SEED_IP:26657/status"
SEED_ID=$(curl -s "$SEED_STATUS_URL" | jq -r '.result.node_info.id')
echo "SEED_ID=$SEED_ID"
export SEEDS="$SEED_ID@$SEED_IP:26656"
echo "SEEDS=$SEEDS"

if [ "$mode" == "local" ]; then
  project_name="$KEY_NAME"

  docker compose -p "$project_name" down -v
  rm -r ./prod-local/"$project_name" || true
else
  project_name="inferenced"
fi

echo "project_name=$project_name"

docker compose -p "$project_name" -f "$compose_file" up -d

# Some time to join chain
sleep 20

# Set node config
curl -X POST "http://localhost:$PORT/v1/nodes/batch" -H "Content-Type: application/json" -d @$NODE_CONFIG

if [ "$mode" == "local" ]; then
  node_container_name="$KEY_NAME-node"
else
  node_container_name="node"
fi
echo "node_container_name=$node_container_name"

# Now get info for adding self
keys_output=$(docker exec "$node_container_name" inferenced keys show $KEY_NAME --output json)

pubkey=$(echo $keys_output | jq -r '.pubkey')
address=$(echo $keys_output | jq -r '.address')
raw_key=$(echo $pubkey | jq -r '.key')

echo "address=$address"
echo "pubkey=$pubkey"
echo "raw_key=$raw_key"

# Run the docker exec command and capture the validator_output
validator_output=$(docker exec "$node_container_name" inferenced tendermint show-validator)

# Use jq to parse the JSON and extract the "key" value
validator_key=$(echo $validator_output | jq -r '.key')

echo "validator_key=$validator_key"

# Use jq to extract unique model values
unique_models=$(jq '[.[] | .models[]] | unique' $NODE_CONFIG)

# Print the unique models
echo "Unique models: $unique_models"

# Prepare the data structure for the final POST
post_data=$(jq -n \
  --arg address "$address" \
  --arg url "$PUBLIC_URL" \
  --argjson models "$unique_models" \
  --arg validator_key "$validator_key" \
  --arg pub_key "$raw_key" \
  '{
    address: $address,
    url: $url,
    models: $models,
    validator_key: $validator_key,
    pub_key: $pub_key
  }')

ADD_ENDPOINT="http://$SEED_IP:8080"
echo "POST request sent to $ADD_ENDPOINT with the following data:"
echo "$post_data"

# Make the final POST request to the ADD_ENDPOINT
curl -X POST "$ADD_ENDPOINT/v1/participants" -H "Content-Type: application/json" -d "$post_data"
