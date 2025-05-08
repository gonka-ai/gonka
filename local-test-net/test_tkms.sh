set -e

docker compose -p join2 down

export SEED_API_URL="http://genesis-api:9000"
export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"
export IS_GENESIS=false

export KEY_NAME=join2
export EXPLORER_PORT=26660
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
export PUBLIC_SERVER_PORT=9020
export ML_SERVER_PORT=9021
export ADMIN_SERVER_PORT=9022
export WIREMOCK_PORT=8092
export RPC_PORT=8102
export P2P_PORT=8202
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
./launch_network_node.sh
