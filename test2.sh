set -e

docker compose -p genesis down
docker compose -p join1 down
docker compose -p join2 down

make build-docker

export PORT=8080
export KEY_NAME=genesis
export NODE_CONFIG=node_payload.json
export EXTERNAL_SEED_IP="0.0.0.0"
./launch_chain.sh local

export ADD_ENDPOINT="http://0.0.0.0:$PORT"
export SEED_IP="genesis-node"

export KEY_NAME=join1
export PORT=8081
./launch_chain.sh local

export KEY_NAME=join2
export PORT=8082
./launch_chain.sh local


if [ "$(whoami)" = "johnlong" ]; then
  curl -X POST "https://maker.ifttt.com/trigger/pushover_alert/with/key/bSVa981BFD2BtZZhn3DnTe?value1=TestRead&value2=Inference-ignite"
fi