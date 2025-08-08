#!/bin/bash
set -e
set -x

source test-utils.sh
export BASE_DIR="./multigen-tests"

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

# Run keygen for each validator
for i in $(seq 0 $(($NUM_VALIDATORS - 1))); do
  # Tear down any existing containers
  docker compose -p "genesis-$i" down

  echo "--- Generating keys for validator $i ---"
  GENESIS_INDEX="$i"
  echo "GENESIS_INDEX=$GENESIS_INDEX"
  export GENESIS_INDEX

  init_ports "$i"
  ./stage-3-gentx.sh

  # Optional: stop the containers if you want to run them one by one
  # docker-compose -f docker-compose.multigen.yml down

  echo "--- Keys for validator $i generated ---"
done

echo "All keys generated."
echo "You can find the artifacts in the '$BASE_DIR/artifacts' directory."
echo "Account and validator keys are in the respective '$BASE_DIR/node*/' directories."
