docker compose -p genesis down
docker compose -p join1 down
docker compose -p join2 down

set -e

make build-docker

export PORT=8080
export KEY_NAME=genesis
export NODE_CONFIG=node_payload_2.json
# BASE_DIR="prod-local/${KEY_NAME}"
export PUBLIC_IP="${KEY_NAME}-api"
rm -r "prod-local" || true
export DAPI_API__PUBLIC_URL="http://$PUBLIC_IP:$PORT"
export DAPI_API__POC_CALLBACK_URL="$DAPI_API__PUBLIC_URL"
export IS_GENESIS=true

docker compose -p genesis -f docker-compose-local-genesis.yml up -d
sleep 20

export KEY_NAME=join1
export NODE_CONFIG=$NODE_CONFIG
export ADD_ENDPOINT="http://0.0.0.0:$PORT"
export PUBLIC_IP="join1-api"
export PORT=8081
export WIREMOCK_PORT=8091
export SEED_IP="genesis-node"
export EXTERNAL_SEED_IP="0.0.0.0"
export SEED_API_URL="http://$SEED_IP:8080"
export SEED_NODE_RPC_URL="http://$SEED_IP:26657"
export SEED_NODE_P2P_PORT=26656
export IS_GENESIS=false
./launch_chain.sh local

export KEY_NAME=join2
export PORT=8082
export WIREMOCK_PORT=8092
export PUBLIC_IP="join2-api"
./launch_chain.sh local


if [ "$(whoami)" = "johnlong" ]; then
  curl -X POST "https://maker.ifttt.com/trigger/pushover_alert/with/key/bSVa981BFD2BtZZhn3DnTe?value1=TestRead&value2=Inference-ignite"
fi
