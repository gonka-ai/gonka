#!/bin/sh
set -e

export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"

make build-docker
