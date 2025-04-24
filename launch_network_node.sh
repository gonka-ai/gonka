#!/bin/bash
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
# PORT - the port to use for the API
# PUBLIC_IP - the access point for getting to your API node from the public

# Much easier to manage the environment variables in a file
# Check if /config.env exists, then source it
if [ -f config.env ]; then
  echo "Sourcing config.env file..."
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

if [ -z "$PUBLIC_SERVER_PORT" ]; then
  echo "PUBLIC_SERVER_PORT is not set"
  exit 1
fi

if [ -z "$WIREMOCK_PORT" ]; then
  WIREMOCK_PORT=$((PUBLIC_SERVER_PORT + 30))
  echo "WIREMOCK_PORT is not set, using $WIREMOCK_PORT"
fi

if [ "$mode" == "local" ]; then
  project_name="$KEY_NAME"
else
  project_name="inferenced"
fi

echo "project_name=$project_name"

# Set up wiremock
if [ "$mode" == "local" ]; then
  mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
  mkdir -p "./prod-local/wiremock/$KEY_NAME/__files/"
  cp ./testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"

  # If there's anything in the public-html/ dir, copy it!
  if [ -n "$(ls -A ./public-html 2>/dev/null)" ]; then
    cp -r ./public-html/* "./prod-local/wiremock/$KEY_NAME/__files/"
  fi
fi

#!!!
docker compose -p "$project_name" -f "$compose_file" up -d
