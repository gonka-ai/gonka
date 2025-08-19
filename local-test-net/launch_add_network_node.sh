#!/bin/bash
set -e

# Verify parameters:
# KEY_NAME - name of the key pair to use
# NODE_CONFIG - name of a file with inference node configuration
# PUBLIC_SERVER_PORT - the port to use for the API
# PUBLIC_IP - the access point for getting to your API node from the public
# PROXY_ACTIVE - set to "true" to include proxy service (optional)
# BRIDGE_ACTIVE - set to "true" to include bridge service (optional)

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

if [ -z "$P2P_EXTERNAL_ADDRESS" ]; then
  echo "P2P_EXTERNAL_ADDRESS is not set"
  exit 1
fi

project_name="$KEY_NAME"

docker compose -p "$project_name" down -v
rm -r ./prod-local/"$project_name" || true

echo "project_name=$project_name"

# Set up wiremock
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
mkdir -p "./prod-local/wiremock/$KEY_NAME/__files/"
cp ../testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"
sed "s/{{KEY_NAME}}/$KEY_NAME/g" ../testermint/src/main/resources/alternative-mappings/validate_poc_batch.template.json > "./prod-local/wiremock/$KEY_NAME/mappings/validate_poc_batch.json"

# If there's anything in the public-html/ dir, copy it!
if [ -n "$(ls -A ./public-html 2>/dev/null)" ]; then
  cp -r ../public-html/* "./prod-local/wiremock/$KEY_NAME/__files/"
fi


# Build compose command with conditional services support
COMPOSE_FILES="-f docker-compose-base.yml -f docker-compose.join.yml"
if [ "${PROXY_ACTIVE}" = "true" ]; then
  COMPOSE_FILES="$COMPOSE_FILES -f docker-compose.proxy.yml"
  echo "Starting with proxy support"
fi
if [ "${BRIDGE_ACTIVE}" = "true" ]; then
  COMPOSE_FILES="$COMPOSE_FILES \
  -f docker-compose.bridge.yml \
  -f docker-compose.contracts.yml"
  echo "Starting with bridge support"
fi

docker compose -p "$project_name" $COMPOSE_FILES up -d
