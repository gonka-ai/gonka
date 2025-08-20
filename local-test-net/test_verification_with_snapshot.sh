export SEED_API_URL="http://genesis-api:9000"
export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"
export IS_GENESIS=false

export KEY_NAME=join2
export RPC_SERVER_URL_2="http://genesis-node:26657"
export RPC_SERVER_URL_1="http://join1-node:26657"
export SYNC_WITH_SNAPSHOTS="true"
export NODE_CONFIG="node_payload_mock_server_${KEY_NAME}.json"
export PUBLIC_SERVER_PORT=9020
export ML_SERVER_PORT=9021
export ADMIN_SERVER_PORT=9022
export ML_GRPC_SERVER_PORT=9023
export WIREMOCK_PORT=8092
export RPC_PORT=8102
export P2P_PORT=8202
export PUBLIC_URL="http://${KEY_NAME}-api:9020"
export POC_CALLBACK_URL="http://${KEY_NAME}-api:9100"
export P2P_EXTERNAL_ADDRESS="http://${KEY_NAME}-node:26656"
export GENESIS_APP_HASH="E811D0B337603B12722EBD549C588354FB8127A364D435CFB1BA389B096362E8"
./launch_add_network_node.sh
