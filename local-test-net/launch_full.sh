# This script runs 1 genesis node with explorer and proxy, and 2 join nodes with proxy
# Proxy ports: genesis=80, join1=81, join2=82
set -e

export REST_API_ACTIVE=true
export PUBLIC_SERVER_PORT=9000
export ML_SERVER_PORT=9001
export ADMIN_SERVER_PORT=9002
export ML_GRPC_SERVER_PORT=9003
export KEY_NAME=genesis
export NODE_CONFIG="node_payload_mock-server_${KEY_NAME}.json"
rm -r "prod-local" || true
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export IS_GENESIS=true
export WIREMOCK_PORT=8090
export PROXY_PORT=80
export DASHBOARD_PORT=5173  # Enable dashboard for proxy
# Unique internal bridge ports per node (no host exposure)
export GETH_HTTP_PORT=8545
export GETH_AUTHRPC_PORT=8551
export GETH_P2P_PORT=30303
export GETH_DISCOVERY_PORT=30303
export PRYSM_P2P_TCP_PORT=13000
export PRYSM_P2P_UDP_PORT=12000
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
mkdir -p "./prod-local/wiremock/$KEY_NAME/__files/"
cp ../testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"
sed "s/{{KEY_NAME}}/$KEY_NAME/g" ../testermint/src/main/resources/alternative-mappings/validate_poc_batch.template.json > "./prod-local/wiremock/$KEY_NAME/mappings/validate_poc_batch.json"
if [ -n "$(ls -A ./public-html 2>/dev/null)" ]; then
  cp -r ../public-html/* "./prod-local/wiremock/$KEY_NAME/__files/"
fi

echo "Starting genesis node with explorer and proxy (port ${PROXY_PORT})"
docker compose -p genesis \
  -f docker-compose-base.yml \
  -f docker-compose.genesis.yml \
  -f docker-compose.explorer.yml \
  -f docker-compose.proxy.yml \
  -f docker-compose.bridge.yml \
  -f docker-compose.contracts.yml \
  up -d
sleep 40

# seed node parameters for both joining nodes
export SEED_API_URL="http://genesis-api:9000"
export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"
export IS_GENESIS=false

# join node 'join1' with proxy
export KEY_NAME=join1
export NODE_CONFIG="node_payload_mock-server_${KEY_NAME}.json"
export PUBLIC_IP="join1-api"
export PUBLIC_SERVER_PORT=9010
export ML_SERVER_PORT=9011
export ADMIN_SERVER_PORT=9012
export ML_GRPC_SERVER_PORT=9013
export WIREMOCK_PORT=8091
export RPC_PORT=8101
export P2P_PORT=8201
export PROXY_PORT=81
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export P2P_EXTERNAL_ADDRESS="http://${KEY_NAME}-node:26656"
export PROXY_ACTIVE=true
export BRIDGE_ACTIVE=true
# Unique internal bridge ports for join1
export GETH_HTTP_PORT=8555
export GETH_AUTHRPC_PORT=8561
export GETH_P2P_PORT=30313
export GETH_DISCOVERY_PORT=30313
export PRYSM_P2P_TCP_PORT=13010
export PRYSM_P2P_UDP_PORT=12010
# Don't set DASHBOARD_PORT for join nodes - they don't have explorer
unset DASHBOARD_PORT
./launch_add_network_node.sh

# join node 'join2' with proxy
export KEY_NAME=join2
export NODE_CONFIG="node_payload_mock-server_${KEY_NAME}.json"
export PUBLIC_SERVER_PORT=9020
export ML_SERVER_PORT=9021
export ADMIN_SERVER_PORT=9022
export ML_GRPC_SERVER_PORT=9023
export WIREMOCK_PORT=8092
export RPC_PORT=8102
export P2P_PORT=8202
export PROXY_PORT=82
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export P2P_EXTERNAL_ADDRESS="http://${KEY_NAME}-node:26656"
export PROXY_ACTIVE=true
export BRIDGE_ACTIVE=true
# Unique internal bridge ports for join2
export GETH_HTTP_PORT=8565
export GETH_AUTHRPC_PORT=8571
export GETH_P2P_PORT=30323
export GETH_DISCOVERY_PORT=30323
export PRYSM_P2P_TCP_PORT=13020
export PRYSM_P2P_UDP_PORT=12020
# Don't set DASHBOARD_PORT for join nodes - they don't have explorer
unset DASHBOARD_PORT
./launch_add_network_node.sh 