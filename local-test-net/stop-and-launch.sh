set -e

./stop.sh

export GENESIS_OVERRIDES_FILE="../inference-chain/test_genesis_overrides.json"
export SET_LATEST=1
make -C ../. build-docker

./launch.sh

echo "Local chain ready"
