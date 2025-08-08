#!/bin/bash
set -e
set -x

# Number of validators to generate keys for
NUM_VALIDATORS=${1:-3}

# Base directory for test artifacts
BASE_DIR="multigen-tests"

echo "Preparing directories for $NUM_VALIDATORS validators..."

# Clean up previous run
rm -rf $BASE_DIR
mkdir -p $BASE_DIR/artifacts

# Create directories for each node
for i in $(seq 0 $(($NUM_VALIDATORS - 1))); do
  mkdir -p "$BASE_DIR/node$i/keyring"
  mkdir -p "$BASE_DIR/node$i/tmkms"
done

echo "Running keygen for each validator..."

# Run keygen for each validator
for i in $(seq 0 $(($NUM_VALIDATORS - 1))); do
  echo "--- Generating keys for validator $i ---"
  
  export GENESIS_INDEX=$i
  export KEY_NAME="validator$i"
  
  docker-compose -f docker-compose.multigen.yml up --build
  
  # Optional: stop the containers if you want to run them one by one
  # docker-compose -f docker-compose.multigen.yml down
  
  echo "--- Keys for validator $i generated ---"
done

echo "All keys generated."
echo "You can find the artifacts in the '$BASE_DIR/artifacts' directory."
echo "Account and validator keys are in the respective '$BASE_DIR/node*/' directories."

