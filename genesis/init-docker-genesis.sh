#!/bin/sh
set -e
set -x

filter_cw20_code() {
  input=$(cat)
  # Remove cw20_code field and its value using sed
  echo "$input" | sed -n -E '
    # If we find cw20_code, skip until the next closing brace
    /[[:space:]]*"cw20_code":[[:space:]]*"/ {
      :skip
      n
      /^[[:space:]]*}[,}]?$/! b skip
      n
    }
    # Print all other lines
    p
  '
}

if [ -z "$KEYRING_BACKEND" ]; then
  echo "KEYRING_BACKEND is not specified defaulting to test"
  KEYRING_BACKEND="test"
fi

# Display the parsed values (for debugging)
echo "Using the following arguments"
echo "KEYRING_BACKEND: $KEYRING_BACKEND"

KEY_NAME="genesis"
APP_NAME="inferenced"
CHAIN_ID="gonka-mainnet"
COIN_DENOM="ngonka"
STATE_DIR="$HOME/.inference"


output=$($APP_NAME init \
  --chain-id "$CHAIN_ID" \
  --default-denom $COIN_DENOM \
  my-node 2>&1)
exit_code=$?
if [ $exit_code -ne 0 ]; then
    echo "Error: '$APP_NAME init' failed with exit code $exit_code"
    echo "Output:"
    echo "$output"
    exit $exit_code
fi
echo "$output" | filter_cw20_code

echo "Setting the chain configuration"

SNAPSHOT_INTERVAL=${SNAPSHOT_INTERVAL:-10}
SNAPSHOT_KEEP_RECENT=${SNAPSHOT_KEEP_RECENT:-5}

$APP_NAME config set client chain-id $CHAIN_ID
$APP_NAME config set client keyring-backend $KEYRING_BACKEND
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"
$APP_NAME config set app state-sync.snapshot-interval $SNAPSHOT_INTERVAL
$APP_NAME config set app state-sync.snapshot-keep-recent $SNAPSHOT_KEEP_RECENT

echo "Setting the node configuration (config.toml)"
if [ -n "$P2P_EXTERNAL_ADDRESS" ]; then
  echo "Setting the external address for P2P to $P2P_EXTERNAL_ADDRESS"
  $APP_NAME config set config p2p.external_address "$P2P_EXTERNAL_ADDRESS" --skip-validate
else
  echo "P2P_EXTERNAL_ADDRESS is not set, skipping"
fi

sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' \
  $STATE_DIR/config/config.toml
# no seeds for genesis node
sed -Ei "s/^seeds = .*$/seeds = \"\"/g" \
  $STATE_DIR/config/config.toml
#sed -Ei 's/^log_level = "info"$/log_level = "debug"/g' $STATE_DIR/config/config.toml
#if [ -n "${DEBUG-}" ]; then
#  sed -i 's/^log_level = "info"/log_level = "debug"/' "$STATE_DIR/config/config.toml"
#fi


modify_genesis_file() {
  local json_file="$STATE_DIR/config/genesis.json"
  local override_file="$1"


  if [ ! -f "$override_file" ]; then
    echo "Override file $override_file does not exist. Exiting..."
    return
  fi
  echo "Checking if jq is installed"
  which jq
  jq ". * input" "$json_file" "$override_file" > "${json_file}.tmp"
  mv "${json_file}.tmp" "$json_file"
  echo "Modified $json_file with file: $override_file"
  cat "$json_file" | filter_cw20_code
}

# Usage
modify_genesis_file 'denom.json'
MILLION_BASE="000000$COIN_DENOM"
NATIVE="000000000$COIN_DENOM"
MILLION_NATIVE="000000$NATIVE"


modify_genesis_file 'genesis-overrides.json'
modify_genesis_file "$HOME/.inference/genesis_overrides.json"
echo "Genesis file created"