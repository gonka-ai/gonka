#!/bin/bash
set -e
set -x

source test-utils.sh
export BASE_DIR="./multigen-tests"

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

rm -rf "$BASE_DIR/genesis-0/input-artifacts/addresses"
mkdir -p "$BASE_DIR/genesis-0/input-artifacts/addresses"

docker compose -p "genesis-0" down || true

# Copy addresses
for i in $(seq 1 $(($NUM_VALIDATORS - 1))); do
  FROM="$BASE_DIR/genesis-$i/artifacts/address.txt"
  TO="$BASE_DIR/genesis-0/input-artifacts/addresses/address-$i.txt"
  cp "$FROM" "$TO"
  echo "Copied $FROM to $TO"

  FROM="$BASE_DIR/genesis-$i/artifacts/address_warm.txt"
  TO="$BASE_DIR/genesis-0/input-artifacts/addresses/address-$i-warm.txt"
  cp "$FROM" "$TO"
  echo "Copied $FROM to $TO"
done

export GENESIS_INDEX=0
init_ports "$GENESIS_INDEX"
./stage-2-intermediate-genesis.sh
