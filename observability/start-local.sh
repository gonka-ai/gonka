#!/bin/sh
set -e
CHAIN_NODE=genesis-node exec "$(dirname "$0")/start.sh"
