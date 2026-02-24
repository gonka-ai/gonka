set -e

./stop.sh

# Don't need to make path relative to ./local-test-net, becayse make is run with root as workdir
export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
export BLST_PORTABLE=1
export SET_LATEST=1
make -C ../. build-docker
