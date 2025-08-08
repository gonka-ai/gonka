#!/bin/bash
set -e
set -x

source test-utils.sh
export BASE_DIR="./multigen-tests"

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

# Copy all gentx to genesis-0
for i in $(seq 1 $(($NUM_VALIDATORS - 1))); do
  # Copy all
  cp "$BASE_DIR/genesis-$i/node/config/gentx/*.json" "$BASE_DIR/genesis-0/node/config/gentx/."

  # Tear down any existing containers
  docker compose -p "genesis-$i" down
done

init_ports "0"
export GENESIS_INDEX="0"
./stage-4-run.sh
