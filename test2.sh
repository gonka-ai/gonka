docker stop $(docker ps -a -q) # stop all running containers

set -e

export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
make build-docker

./launch-local-test-chain.sh

if [ "$(whoami)" = "johnlong" ]; then
  curl -X POST "https://maker.ifttt.com/trigger/pushover_alert/with/key/bSVa981BFD2BtZZhn3DnTe?value1=TestRead&value2=Inference-ignite"
fi
