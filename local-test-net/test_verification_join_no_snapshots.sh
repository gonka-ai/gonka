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
export GENESIS_APP_HASH="5A1C91002243225023D37ADFCBAB5B147750377B94311ACC813735996C29A557"
./launch_add_network_node.sh