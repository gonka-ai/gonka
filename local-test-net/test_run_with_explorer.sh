set -e

export EXPLORER_PORT=26660
export PUBLIC_SERVER_PORT=9000
export ML_SERVER_PORT=9001
export ADMIN_SERVER_PORT=9002
export KEY_NAME=genesis
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
rm -r "prod-local" || true
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export IS_GENESIS=true
export WIREMOCK_PORT=8090
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
mkdir -p "./prod-local/wiremock/$KEY_NAME/__files/"
cp ../testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"
if [ -n "$(ls -A ./public-html 2>/dev/null)" ]; then
  cp -r ../public-html/* "./prod-local/wiremock/$KEY_NAME/__files/"
fi

echo "Starting genesis node"
docker compose -p genesis -f docker-compose-local-genesis-with-explorer.yml up -d
