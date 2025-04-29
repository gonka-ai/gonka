#!/bin/sh
set -e

# Don't need to make path relative to ./local-test-net, becayse make is run with root as workdir
export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"

make build-docker
