set -e

make stop-test-chain

export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
export SET_LATEST=1
make build-docker

make launch-test-chain

if [ "$(whoami)" = "johnlong" ]; then
  curl -X POST "https://maker.ifttt.com/trigger/pushover_alert/with/key/bSVa981BFD2BtZZhn3DnTe?value1=TestRead&value2=Inference-ignite"
fi
