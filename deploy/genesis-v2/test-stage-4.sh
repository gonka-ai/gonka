#!/bin/bash
set -e
set -x

source test-utils.sh
export HOST_ACCESS_ADDR=${HOST_ACCESS_ADDR:-127.0.0.1}
export BASE_DIR="./multigen-tests"

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

rm -rf "$BASE_DIR/genesis-0/input-artifacts/gentx"
mkdir -p "$BASE_DIR/genesis-0/input-artifacts/gentx"

# Copy all gentx to genesis-0
for i in $(seq 0 $(($NUM_VALIDATORS - 1))); do
  # Copy all
  cp "$BASE_DIR/genesis-$i/node/config/gentx"/*.json "$BASE_DIR/genesis-0/input-artifacts/gentx/."

  # Tear down any existing containers
  docker compose -p "genesis-$i" down
done

init_ports "0"
export GENESIS_INDEX="0"
./stage-4-start.sh

sleep 30

for i in $(seq 1 $(($NUM_VALIDATORS - 1))); do
  cp "$BASE_DIR/genesis-0/artifacts/genesis-final.json" "$BASE_DIR/genesis-$i/input-artifacts/gentx/genesis-final.json"

  init_ports "$i"
  export GENESIS_INDEX="$i"
  ./stage-4-start.sh
done
