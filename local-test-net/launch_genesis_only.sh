set -e

export REST_API_ACTIVE=true
export DASHBOARD_PORT=5173
export PUBLIC_SERVER_PORT=9000
export ML_SERVER_PORT=9001
export ADMIN_SERVER_PORT=9002
export KEY_NAME=genesis
export NODE_CONFIG="node_payload_mock-server_${KEY_NAME}.json"
rm -r "prod-local" || true
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export IS_GENESIS=true
export WIREMOCK_PORT=8090
export PROXY_PORT=80
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
mkdir -p "./prod-local/wiremock/$KEY_NAME/__files/"
cp ../testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"
if [ -n "$(ls -A ./public-html 2>/dev/null)" ]; then
  cp -r ../public-html/* "./prod-local/wiremock/$KEY_NAME/__files/"
fi

echo "Starting genesis node with explorer"
docker compose -p genesis \
-f docker-compose-base.yml \
-f docker-compose.genesis.yml \
-f docker-compose.explorer.yml \
-f docker-compose.proxy.yml \
-f docker-compose.contracts.yml \
-f docker-compose.bridge.yml \
up -d
