set -e

mode="$1"
if [ -z "$mode" ]; then
  mode="local"
fi

if [ "$mode" == "local" ]; then
  # TODO: there's no such file yet
  compose_file="docker-compose-local-genesis.yml"
elif [ "$mode" == "cloud" ]; then
  compose_file="docker-compose-cloud-genesis.yml"
else
  echo "Unknown mode: $mode"
  exit 1
fi

# Verify parameters:
# KEY_NAME - name of the key pair to use
# NODE_CONFIG - name of a file with inference node configuration
# ADD_ENDPOINT - the endpoint to use for adding unfunded participant
# PORT - the port to use for the API
# PUBLIC_URL - the access point for getting to your API node from the public
# SEEDS - the list of seed nodes to connect to

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

if [ -z "$PORT" ]; then
  echo "PORT is not set"
  exit 1
fi

if [ -z "$PUBLIC_URL" ]; then
  echo "PUBLIC_URL is not set"
  exit 1
fi

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

curl -X POST "http://localhost:$PORT/v1/nodes/batch" -H "Content-Type: application/json" -d @$NODE_CONFIG

node_container_name="$project_name-node"
echo "node_container_name=$node_container_name"

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
  --arg url "$PUBLIC_URL" \
  --argjson models "$unique_models" \
  --arg validator_key "$validator_key" \
  '{
    url: $url,
    models: $models,
    validator_key: $validator_key,
  }')

echo "POST request sent to http://localhost:$PORT with the following data:"
echo "$post_data"

curl -X POST "http://localhost:$PORT/v1/participants" -H "Content-Type: application/json" -d "$post_data"
