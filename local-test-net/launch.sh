# This script runs 1 genesis node, which is used as seed node also, and 2 full nodes
set -e

# launch genesis node
export PUBLIC_SERVER_PORT=9000
export ML_SERVER_PORT=9001
export ADMIN_SERVER_PORT=9002
export ML_GRPC_SERVER_PORT=9003
export KEY_NAME=genesis
export NODE_CONFIG="node_payload_mock_server_${KEY_NAME}.json"
rm -r "prod-local" || true
export PUBLIC_URL="http://${KEY_NAME}-api:9000"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export IS_GENESIS=true
export WIREMOCK_PORT=8090
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
mkdir -p "./prod-local/wiremock/$KEY_NAME/__files/"
cp ../testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"
sed "s/{{KEY_NAME}}/$KEY_NAME/g" ../testermint/src/main/resources/alternative-mappings/validate_poc_batch.template.json > "./prod-local/wiremock/$KEY_NAME/mappings/validate_poc_batch.json"
if [ -n "$(ls -A ./public-html 2>/dev/null)" ]; then
  cp -r ../public-html/* "./prod-local/wiremock/$KEY_NAME/__files/"
fi
export WIREMOCK_PORT_2=8089
mkdir -p "./prod-local/wiremock-2/$KEY_NAME/mappings/"
mkdir -p "./prod-local/wiremock-2/$KEY_NAME/__files/"
cp ../testermint/src/main/resources/mappings/*.json "./prod-local/wiremock-2/$KEY_NAME/mappings/"
cp ../testermint/src/main/resources/alternative-mappings/generate_poc.json "./prod-local/wiremock-2/$KEY_NAME/mappings/generate_poc.json"
if [ -n "$(ls -A ./public-html 2>/dev/null)" ]; then
  cp -r ../public-html/* "./prod-local/wiremock-2/$KEY_NAME/__files/"
fi

echo "Starting genesis node"
docker compose -p genesis -f docker-compose-base.yml -f docker-compose.genesis.yml up -d
sleep 40

# seed node parameters for both joining nodes
export SEED_API_URL="http://genesis-api:9000"
export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"
export IS_GENESIS=false

# join node 'join1'
export KEY_NAME=join1
export NODE_CONFIG="node_payload_mock_server_${KEY_NAME}.json"
export PUBLIC_IP="join1-api"
export PUBLIC_SERVER_PORT=9010
export ML_SERVER_PORT=9011
export ADMIN_SERVER_PORT=9012
export ML_GRPC_SERVER_PORT=9013
export NATS_SERVER_PORT=9014
export WIREMOCK_PORT=8091
export RPC_PORT=8101
export P2P_PORT=8201
export PUBLIC_URL="http://${KEY_NAME}-api:9010"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export P2P_EXTERNAL_ADDRESS="http://${KEY_NAME}-node:26656"
./launch_add_network_node.sh

# join node 'join2'
export KEY_NAME=join2
export NODE_CONFIG="node_payload_mock_server_${KEY_NAME}.json"
export PUBLIC_SERVER_PORT=9020
export ML_SERVER_PORT=9021
export ADMIN_SERVER_PORT=9022
export ML_GRPC_SERVER_PORT=9023
export NATS_SERVER_PORT=9024
export WIREMOCK_PORT=8092
export RPC_PORT=8102
export P2P_PORT=8202
export PUBLIC_URL="http://${KEY_NAME}-api:9020"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export P2P_EXTERNAL_ADDRESS="http://${KEY_NAME}-node:26656"
./launch_add_network_node.sh
