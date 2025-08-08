#!/bin/bash
set -e
set -x

source test-utils.sh
export BASE_DIR="./multigen-tests"

export GENESIS_INDEX=0
./stage-2-intermediate-genesis.sh
