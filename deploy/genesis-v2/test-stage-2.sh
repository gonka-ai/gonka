#!/bin/bash
set -e
set -x

source test-utils.sh
export BASE_DIR="./multigen-tests"

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

# Copy addresses
for i in $(seq 0 $(($NUM_VALIDATORS - 1))); do
  DIR="$BASE_DIR/genesis-$i"

  cp "$DIR/genesis-$i/artifacts/address.txt" "$BASE_DIR/genesis-0/addresses/address-$i.txt"
done

export GENESIS_INDEX=0
init_ports "$GENESIS_INDEX"
./stage-2-intermediate-genesis.sh
