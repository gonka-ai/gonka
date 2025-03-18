set -e

make stop-test-chain

export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
export SET_LATEST=1
make build-docker

make launch-test-chain

echo "Local chain ready"