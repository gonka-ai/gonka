set -e

docker stop join3-wiremock
docker stop join3-node
docker stop join3-api

export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"

export RPC_SERVER_URL_1="http://join1-node:26657"
export RPC_SERVER_URL_2="http://join2-node:26657"

export SYNC_WITH_SNAPSHOTS=true
export KEY_NAME=join3
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
export PORT=8083
export WIREMOCK_PORT=8093
export RPC_PORT=8103
export P2P_PORT=8203
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="$PUBLIC_URL"
./launch_network_node.sh local