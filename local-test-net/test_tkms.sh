set -e

docker compose -p join1 down

export SEED_API_URL="http://genesis-api:9000"
export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"
export IS_GENESIS=false

export KEY_NAME=join1
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
export PUBLIC_IP="join1-api"
export PUBLIC_SERVER_PORT=9010
export ML_SERVER_PORT=9011
export ADMIN_SERVER_PORT=9012
export WIREMOCK_PORT=8091
export RPC_PORT=8101
export P2P_PORT=8201
export TKMS_PORT=26658
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
./launch_network_node.sh local-tkms