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

if [ -z "$PORT" ]; then
  echo "PORT is not set"
  exit 1
fi

if [ -v "$SEED_IP" ]; then
   SEED_STATUS_URL="http://$SEED_IP:26657/status"
   SEED_ID=$(curl -s "$SEED_STATUS_URL" | jq -r '.result.node_info.id')
   echo "SEED_ID=$SEED_ID"
   export SEEDS="$SEED_ID@$SEED_IP:26656"
   echo "SEEDS=$SEEDS"
fi

if [ "$mode" == "local" ]; then
  project_name="$KEY_NAME"

  docker compose -p "$project_name" down -v
  rm -r ./prod-local/"$project_name" || true
else
  project_name="inferenced"
fi

echo "project_name=$project_name"

export GENESIS_FILE="genesis.json"
docker compose -p "$project_name" -f "$compose_file" -f docker-compose-cloud-restart.yml up -d
