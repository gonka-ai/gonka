docker compose -p genesis down
docker compose -p join1 down
docker compose -p join2 down

set -e

export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"

make build-docker

export PORT=8080
export KEY_NAME=genesis
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
# BASE_DIR="prod-local/${KEY_NAME}"
rm -r "prod-local" || true
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="$PUBLIC_URL"
export IS_GENESIS=true
export WIREMOCK_PORT=8090
mkdir -p "./prod-local/wiremock/$KEY_NAME/mappings/"
cp ./testermint/src/main/resources/mappings/*.json "./prod-local/wiremock/$KEY_NAME/mappings/"

echo "Starting genesis node"
docker compose -p genesis -f docker-compose-local-genesis.yml up -d
sleep 40

export KEY_NAME=join1
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
export PUBLIC_IP="join1-api"
export PORT=8081
export WIREMOCK_PORT=8091
export SEED_API_URL="http://genesis-api:8080"
export SEED_NODE_RPC_URL="http://genesis-node:26657"
export SEED_NODE_P2P_URL="http://genesis-node:26656"
export IS_GENESIS=false
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="$PUBLIC_URL"
./launch_chain.sh local

export KEY_NAME=join2
export NODE_CONFIG="node_payload_wiremock_${KEY_NAME}.json"
export PORT=8082
export WIREMOCK_PORT=8092
export PUBLIC_URL="http://${KEY_NAME}-api:8080"
export POC_CALLBACK_URL="$PUBLIC_URL"
./launch_chain.sh local

if [ "$(whoami)" = "johnlong" ]; then
  curl -X POST "https://maker.ifttt.com/trigger/pushover_alert/with/key/bSVa981BFD2BtZZhn3DnTe?value1=TestRead&value2=Inference-ignite"
fi
