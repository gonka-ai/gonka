# This script runs 1 genesis node, which is used as seed node also, and 2 full nodes
set -e

# launch genesis node
export PORT=8080
export KEY_NAME=genesis
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
rm -r "prod-local" || true
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="$PUBLIC_URL"
export IS_GENESIS=true
export WIREMOCK_PORT=8090
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
mkdir -p "./prod-local/wiremock/$KEY_NAME/__files/"
cp ./testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"
# cp -r ./public-html/* "./prod-local/wiremock/$KEY_NAME/__files/"

echo "Starting genesis node"
docker compose -p genesis -f docker-compose-local-genesis.yml up -d
sleep 40

# seed node parameters for both joining nodes
export SEED_API_URL="http://genesis-api:8080"
export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"
export IS_GENESIS=false

# join node 'join1'
export KEY_NAME=join1
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
export PUBLIC_IP="join1-api"
export PORT=8081
export WIREMOCK_PORT=8091
export RPC_PORT=8101
export P2P_PORT=8201
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="$PUBLIC_URL"
./launch_network_node.sh local

# join node 'join2'
export KEY_NAME=join2
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
export PORT=8082
export WIREMOCK_PORT=8092
export RPC_PORT=8102
export P2P_PORT=8202
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="$PUBLIC_URL"
./launch_network_node.sh local