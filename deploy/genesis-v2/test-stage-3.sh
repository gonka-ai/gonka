#!/bin/bash
set -e
set -x

source test-utils.sh
export BASE_DIR="./multigen-tests"

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

GENESIS_PATH="$BASE_DIR/genesis-0/artifacts/genesis-draft.json"
# Distribute genesis
for i in $(seq 0 $(($NUM_VALIDATORS - 1))); do
  cp "$GENESIS_PATH" "$BASE_DIR/genesis-$i/input-artifacts/genesis.json"
  transform_pubkey "$BASE_DIR/genesis-$i/artifacts/validator_pubkey.json"
  # TODO: get the result of the function and put it in the input-artifacts directory

  # Tear down any existing containers
  docker compose -p "genesis-$i" down

  echo "--- Generating keys for validator $i ---"
  GENESIS_INDEX="$i"
  echo "GENESIS_INDEX=$GENESIS_INDEX"
  export GENESIS_INDEX

  init_ports "$i"
  ./stage-3-gentx.sh

  echo "--- Keys for validator $i generated ---"
done

echo "All keys generated."
echo "You can find the artifacts in the '$BASE_DIR/artifacts' directory."
echo "Account and validator keys are in the respective '$BASE_DIR/node*/' directories."
