#!/bin/bash

init_ports() {
  if [ -z "$1" ]; then
    echo "Error: node index not provided." >&2
    echo "Usage: init_ports <node_index>" >&2
    return 1
  fi

  local node_index=$1

  if ! [[ "$node_index" =~ ^[0-9]+$ ]]; then
    echo "Error: node index must be a non-negative integer." >&2
    return 1
  fi

  export API_PORT=$((8000 + node_index * 10))
  export P2P_PORT=$((26656 + node_index * 10))
  export RPC_PORT=$((26657 + node_index * 10))
}
