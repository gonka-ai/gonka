set -e

make -C ../. stop-test-chain

export GENESIS_OVERRIDES_FILE="../inference-chain/test_genesis_overrides.json"
export SET_LATEST=1
make -C ../. build-docker

make -C ../. launch-test-chain

echo "Local chain ready"