set -e

mode="$1"
if [ -z "$mode" ]; then
  mode="local"
fi

if [ "$mode" == "local" ]; then
  compose_file="docker-compose-local.yml"
elif [ "$mode" == "cloud" ]; then
  compose_file="docker-compose-cloud-join.yml"
elif [ "$mode" == "cloud-genesis" ]; then
  compose_file="docker-compose-cloud-genesis.yml"
else
  echo "Unknown mode: $mode"
  exit 1
fi

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
