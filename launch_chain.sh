set -e

mode="$1"
if [ -z "$mode" ]; then
  mode="local"
fi

if [ "$mode" == "local" ]; then
  if [ "$KEY_NAME" == "genesis" ]; then
    compose_file="docker-compose-local-genesis.yml"
  else
    compose_file="docker-compose-local.yml"
  fi
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
# PUBLIC_IP - the access point for getting to your API node from the public

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

if [ -z "$SEED_IP" ] && [ "$KEY_NAME" != "genesis" ]; then
  echo "SEED_IP is not set and KEY_NAME is not 'genesis'"
  exit 1
fi

if [ -z "$EXTERNAL_SEED_IP" ] && [ "$KEY_NAME" != "genesis" ]; then
  echo "EXTERNAL_SEED_IP is not set, using SEED_IP"
  export EXTERNAL_SEED_IP="$SEED_IP"
fi

if [ -z "$PORT" ]; then
  echo "PORT is not set"
  exit 1
fi

if [ -z "$WIREMOCK_PORT" ]; then
  WIREMOCK_PORT=$((PORT + 10))
  echo "WIREMOCK_PORT is not set, using $WIREMOCK_PORT"
fi

if [ -z "$PUBLIC_IP" ]; then
  PUBLIC_IP="${KEY_NAME}-api"
fi

export DAPI_API__PUBLIC_URL="http://$PUBLIC_IP:8080"
export DAPI_API__POC_CALLBACK_URL="$DAPI_API__PUBLIC_URL"

if [ "$mode" == "local" ]; then
  project_name="$KEY_NAME"

  docker compose -p "$project_name" down -v
  rm -r ./prod-local/"$project_name" || true
else
  project_name="inferenced"
fi

if [ "$KEY_NAME" != "genesis" ]; then
  SEED_STATUS_URL="http://$EXTERNAL_SEED_IP:26657/status"
  SEED_ID=$(curl -s "$SEED_STATUS_URL" | jq -r '.result.node_info.id')
  echo "SEED_ID=$SEED_ID"
  export SEEDS="$SEED_ID@$SEED_IP:26656"
  echo "SEEDS=$SEEDS"

  GENESIS_URL="http://$EXTERNAL_SEED_IP:26657/genesis"
  export GENESIS_FILE="./prod-local/$KEY_NAME/genesis.json"

  mkdir -p "$(dirname "$GENESIS_FILE")"

  echo "Downloading the genesis file from $GENESIS_URL to $GENESIS_FILE"
  wget -q -O - "$GENESIS_URL" | jq -r '.result.genesis' > "$GENESIS_FILE"

  echo "project_name=$project_name"
fi

#!!!
docker compose -p "$project_name" -f "$compose_file" up -d

# Copy the mappings to the wiremock container
#!!!
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
cp ./testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"

# Some time to join chain
sleep 20

echo "setting node config"
node_post_data=$(jq -n \
  --arg id "wiremock" \
  --arg host "$KEY_NAME-wiremock" \
  --argjson inference_port 8080 \
  --argjson poc_port 8080 \
  --argjson max_concurrent 10 \
  --argjson models '["unsloth/llama-3-8b-Instruct"]' \
  '{
    id: $id,
    host: $host,
    inference_port: $inference_port,
    poc_port: $poc_port,
    max_concurrent: $max_concurrent,
    models: $models
  }')
echo "node data:"
echo "$node_post_data"

# Set node config
#!!!
curl -X POST "http://0.0.0.0:$PORT/v1/nodes" -H "Content-Type: application/json" -d "$node_post_data"

if [ "$mode" == "local" ]; then
  node_container_name="$KEY_NAME-node"
else
  node_container_name="node"
fi
echo "node_container_name=$node_container_name"

# Now get info for adding self
#???
keys_output=$(docker exec "$node_container_name" inferenced keys show $KEY_NAME --output json)

pubkey=$(echo $keys_output | jq -r '.pubkey')
address=$(echo $keys_output | jq -r '.address')
raw_key=$(echo $pubkey | jq -r '.key')

echo "address=$address"
echo "pubkey=$pubkey"
echo "raw_key=$raw_key"

# Run the docker exec command and capture the validator_output
#???
validator_output=$(docker exec "$node_container_name" inferenced tendermint show-validator)

# Use jq to parse the JSON and extract the "key" value
validator_key=$(echo $validator_output | jq -r '.key')

echo "validator_key=$validator_key"

# Use jq to extract unique model values
unique_models=$(jq '[.[] | .models[]] | unique' $NODE_CONFIG)

# Print the unique models
echo "Unique models: $unique_models"
if [ "$KEY_NAME" != "genesis" ]; then
# Prepare the data structure for the final POST
  post_data=$(jq -n \
    --arg address "$address" \
    --arg url "$DAPI_API__PUBLIC_URL" \
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
else
  post_data=$(jq -n \
    --arg url "$DAPI_API__PUBLIC_URL" \
    --argjson models "$unique_models" \
    --arg validator_key "$validator_key" \
    '{
      url: $url,
      models: $models,
      validator_key: $validator_key,
    }')
fi

ADD_ENDPOINT="http://$EXTERNAL_SEED_IP:8080"
echo "POST request sent to $ADD_ENDPOINT with the following data:"
echo "$post_data"

# Make the final POST request to the ADD_ENDPOINT
#!!!
curl -X POST "$ADD_ENDPOINT/v1/participants" -H "Content-Type: application/json" -d "$post_data"
