#!/bin/sh
set -e

if [ -z "$KEYRING_BACKEND" ]; then
  echo "KEYRING_BACKEND is not specified defaulting to test"
  KEYRING_BACKEND="test"
fi

# Display the parsed values (for debugging)
echo "Using the following arguments"
echo "KEYRING_BACKEND: $KEYRING_BACKEND"

KEY_NAME="genesis"
APP_NAME="inferenced"
CHAIN_ID="prod-sim"
COIN_DENOM="nicoin"
STATE_DIR="/root/.inference"

# Init the chain:
# I'm using prod-sim as the chain name (production simulation)
#   and icoin (intelligence coin) as the default denomination
#   and my-node as a node moniker (it doesn't have to be unique)
$APP_NAME init \
  --chain-id "$CHAIN_ID" \
  --default-denom $COIN_DENOM \
  my-node

echo "Setting the chain configuration"
$APP_NAME config set client chain-id $CHAIN_ID
$APP_NAME config set client keyring-backend $KEYRING_BACKEND
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"

echo "Setting the node configuration (config.toml)"
sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' \
  $STATE_DIR/config/config.toml
# no seeds for genesis node
sed -Ei "s/^seeds = .*$/seeds = \"\"/g" \
  $STATE_DIR/config/config.toml
#sed -Ei 's/^log_level = "info"$/log_level = "debug"/g' $STATE_DIR/config/config.toml

echo "Creating the key"
# Create a key
$APP_NAME keys \
    --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR" \
    add "$KEY_NAME"
$APP_NAME keys \
    --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR" \
    add "POOL_product_science_inc"

modify_genesis_file() {
  local json_file="$HOME/.inference/config/genesis.json"
  local override_file="$1"


  if [ ! -f "$override_file" ]; then
    echo "Override file $override_file does not exist. Exiting..."
    return
  fi
  echo "Checking if jq is installed"
  which jq
  jq ". * input" "$json_file" "$override_file" > "${json_file}.tmp"
  mv "${json_file}.tmp" "$json_file"
  echo "Modified $json_file with file: $jq_filter"
  cat "$json_file"
}

# Usage
modify_genesis_file 'denom.json'
MILLION_BASE="000000$COIN_DENOM"
NATIVE="000000000$COIN_DENOM"
MILLION_NATIVE="000000$NATIVE"
echo "Adding the key to the genesis account"
$APP_NAME genesis add-genesis-account "$KEY_NAME" "2$NATIVE" --keyring-backend $KEYRING_BACKEND
$APP_NAME genesis add-genesis-account "POOL_product_science_inc" "160$MILLION_NATIVE" --keyring-backend $KEYRING_BACKEND
$APP_NAME genesis gentx "$KEY_NAME" "1$MILLION_BASE" --chain-id "$CHAIN_ID" || {
  echo "Failed to create gentx"
  tail -f /dev/null
}
$APP_NAME genesis collect-gentxs

modify_genesis_file 'genesis_overrides.json'
modify_genesis_file "$HOME/.inference/genesis_overrides.json"

echo "Genesis file created"
echo "Init for cosmovisor"
cosmovisor init /usr/bin/inferenced || {
  echo "Cosmovisor failed, idling the container..."
  tail -f /dev/null
}

echo "Starting cosmovisor and the chain"
cosmovisor run start || {
  echo "Cosmovisor failed, idling the container..."
  tail -f /dev/null
}
