#!/usr/bin/env bash

INFERENCED_BIN="$1"
if [ -z "$INFERENCED_BIN" ]; then
INFERENCED_BIN="./build/inferenced"
fi

CHAIN_ID="prod-sim"
COIN_DENOM="gonka"
STATE_DIR="$HOME/.inference"

rm -rf "$STATE_DIR"

# genesis node
export SEED_NODE_RPC_URL="http://0.0.0.0:26657"
export SEED_NODE_P2P_URL="http://0.0.0.0:26656"

# 'join1' and 'join2' nodes rpc servers
export RPC_SERVER_URL_1="http://0.0.0.0:8101"
export RPC_SERVER_URL_2="http://0.0.0.0:8102"

# configure inferenced
$INFERENCED_BIN config set client chain-id $CHAIN_ID
$INFERENCED_BIN config set client keyring-backend test
$INFERENCED_BIN keys add alice

$INFERENCED_BIN init \
  --overwrite \
  --chain-id "$CHAIN_ID" \
  --default-denom "$COIN_DENOM" \
  my-node

sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:36657"/g' \
  $STATE_DIR/config/config.toml
sed -Ei 's/^laddr = ".*:26656"$/laddr = "tcp:\/\/0\.0\.0\.0:36656"/g' \
  $STATE_DIR/config/config.toml

# set up parameters to fetch snapshots
$INFERENCED_BIN set-seeds "$STATE_DIR/config/config.toml" "$SEED_NODE_RPC_URL" "$SEED_NODE_P2P_URL"
$INFERENCED_BIN set-statesync "$STATE_DIR/config/config.toml" true
$INFERENCED_BIN set-statesync-rpc-servers "$STATE_DIR/config/config.toml"  "$RPC_SERVER_URL_1" "$RPC_SERVER_URL_2"
$INFERENCED_BIN set-statesync-trusted-block "$STATE_DIR/config/config.toml"  "$SEED_NODE_RPC_URL"

$INFERENCED_BIN config set app minimum-gas-prices "0$COIN_DENOM"
$INFERENCED_BIN config set app state-sync.snapshot-interval 10
$INFERENCED_BIN config set app state-sync.snapshot-keep-recent 2

GENESIS_FILE=$STATE_DIR/config/genesis.json
$INFERENCED_BIN download-genesis "$SEED_NODE_RPC_URL" "$GENESIS_FILE"

cat $GENESIS_FILE

echo "Using genesis file: $GENESIS_FILE"
