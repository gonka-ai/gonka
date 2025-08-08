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
CHAIN_ID="gonka-testnet-7"
COIN_DENOM="nicoin"
STATE_DIR="/root/.inference"
KEYRING_HOME="/root/keyring"

update_configs() {
  if [ "${REST_API_ACTIVE:-}" = true ]; then
    "$APP_NAME" patch-toml "$STATE_DIR/config/app.toml" app_overrides.toml
  else
    echo "Skipping update node config"
  fi
}


# Init the chain:
# I'm using prod-sim as the chain name (production simulation)
#   and icoin (intelligence coin) as the default denomination
#   and my-node as a node moniker (it doesn't have to be unique)
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


if [ "$GENESIS_RUN_STAGE" = "keygen" ]; then
  echo "Creating keys (if they don't exist)..."

  if ! $APP_NAME keys show "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_HOME" >/dev/null 2>&1; then
    echo "Key '$KEY_NAME' not found. Creating..."
    $APP_NAME keys add "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_HOME"
  else
    echo "Key '$KEY_NAME' already exists."
  fi

  # FIXME: should only be created by the 0 genesis
  if ! $APP_NAME keys show "POOL_product_science_inc" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_HOME" >/dev/null 2>&1; then
    echo "Key 'POOL_product_science_inc' not found. Creating..."
    $APP_NAME keys add "POOL_product_science_inc" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_HOME"
  else
    echo "Key 'POOL_product_science_inc' already exists."
  fi
fi

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
  echo "Modified $json_file with file: $override_file"
  cat "$json_file" | filter_cw20_code
}

# Usage
modify_genesis_file 'denom.json'
MILLION_BASE="000000$COIN_DENOM"
NATIVE="000000000$COIN_DENOM"
MILLION_NATIVE="000000$NATIVE"

echo "Adding the keys to the genesis account"
GENESIS_ADDRESS=$($APP_NAME keys show "$KEY_NAME" -a --keyring-backend $KEYRING_BACKEND --keyring-dir "$KEYRING_HOME")
echo "Address for $KEY_NAME is $GENESIS_ADDRESS"
$APP_NAME genesis add-genesis-account "$GENESIS_ADDRESS" "2$NATIVE"

POOL_ADDRESS=$($APP_NAME keys show "POOL_product_science_inc" -a --keyring-backend $KEYRING_BACKEND --keyring-dir "$KEYRING_HOME")
echo "Address for POOL_product_science_inc is $POOL_ADDRESS"
$APP_NAME genesis add-genesis-account "$POOL_ADDRESS" "160$MILLION_NATIVE"

# Add accounts from /root/input-artifacts/addresses
echo "Scanning for genesis accounts in /root/input-artifacts/addresses"
if [ -d "/root/input-artifacts/addresses" ]; then
  echo "Found /root/input-artifacts/addresses directory, adding accounts from there"
  for addr_file in /root/input-artifacts/addresses/*.txt; do
    echo "Processing file: $addr_file"
    if [ -f "$addr_file" ]; then
      address=$(cat "$addr_file")
      echo "Adding genesis account for address $address from $addr_file"
      $APP_NAME genesis add-genesis-account "$address" "2$NATIVE"
    fi
  done
fi

# tgbot
TG_ACC=gonka1va4hlpg929n6hhg4wc8hl0g9yp4nheqxm6k9wr

if [ "$INIT_TGBOT" = "true" ]; then
  echo "Adding the tgbot account"
  $APP_NAME genesis add-genesis-account $TG_ACC "100$MILLION_NATIVE"
fi

modify_genesis_file 'genesis_overrides.json'
modify_genesis_file "$HOME/.inference/genesis_overrides.json"

if [ "$GENESIS_RUN_STAGE" = "keygen" ]; then
    # To do a test keygen run we need a non-empty validator set
    $APP_NAME genesis gentx "$KEY_NAME" "1$MILLION_BASE" --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_HOME" || {
      echo "Failed to create gentx"
      tail -f /dev/null
    }

    output=$($APP_NAME genesis collect-gentxs 2>&1)
    echo "$output" | filter_cw20_code
fi

if [ "$GENESIS_RUN_STAGE" = "genesis-draft" ]; then
  echo "Keygen stage is set, exiting. You can tear down the container now."
  cp /root/.inference/config/genesis.json /root/artifacts/genesis-draft.json
  exit 0
fi

if [ "$GENESIS_RUN_STAGE" = "gentx" ]; then
  if [ ! -f "/root/input-artifacts/genesis.json" ]; then
    echo "Error: /root/input-artifacts/genesis.json is required for the gentx stage, but was not found." >&2
    exit 1
  fi
  echo "Found /root/input-artifacts/genesis.json. Overriding the default genesis file."
  cp "/root/input-artifacts/genesis.json" "/root/.inference/config/genesis.json"

  $APP_NAME genesis gentx "$KEY_NAME" "1$MILLION_BASE" --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_HOME" --pubkey "$(cat /root/input-artifacts/validator_pubkey_formatted.json)" || {
    echo "Failed to create gentx"
    tail -f /dev/null
  }

  echo "Genesis transaction is created. Exiting."
  exit 1
fi

if [ "$GENESIS_RUN_STAGE" = "start" ]; then
  if [ ! -f "/root/input-artifacts/genesis.json" ]; then
    echo "Error: /root/input-artifacts/genesis.json is required for the gentx stage, but was not found." >&2
    exit 1
  fi
  echo "Found /root/input-artifacts/genesis.json. Overriding the default genesis file."
  cp "/root/input-artifacts/genesis.json" "/root/.inference/config/genesis.json"

  if [ "$GENESIS_INDEX" = "0" ]; then
    if [ ! -f "/root/input-artifacts/gentx" ]; then
      echo "Error: /root/input-artifacts/gentx dir is required for the gentx stage, but was not found." >&2
      exit 1
    fi

    rm -rf "$STATE_DIR/config/gentx"
    cp -r /root/input-artifacts/gentx "$STATE_DIR/config/gentx"

    output=$($APP_NAME genesis collect-gentxs 2>&1)
    echo "$output" | filter_cw20_code
  else
    echo "TODO: download genesis.json from the first node!"
  fi
fi

echo "Setting up overrides for config.toml"
 # Process CONFIG_ environment variables
 for var in $(env | grep '^CONFIG_'); do
    # Extract key and value
    key=${var%%=*}
    value=${var#*=}

    # Remove CONFIG_ prefix and transform __ to .
    config_key=${key#CONFIG_}
    config_key=${config_key//__/.}

    echo "Setting config: $config_key = $value"
    $APP_NAME config set config "$config_key" "$value" --skip-validate
 done
# Check and apply config overrides if present
if [ -f "config_override.toml" ]; then
    echo "Applying config overrides from config_override.toml"
    $APP_NAME patch-toml "$STATE_DIR/config/config.toml" config_override.toml
fi

set +e
echo "Key before TMKMS integration"
$APP_NAME tendermint show-validator
cat "$STATE_DIR/config/priv_validator_key.json"
cat "$STATE_DIR/data/priv_validator_state.json"
set -e

# TMKMS integration ------------------------------------------------------------
if [ -n "${TMKMS_PORT-}" ]; then
  echo "Configuring TMKMS (port $TMKMS_PORT)"
  rm -f "$STATE_DIR/config/priv_validator_key.json" \
        "$STATE_DIR/data/priv_validator_state.json"

  sed -i \
    -e "s|^priv_validator_laddr =.*|priv_validator_laddr = \"tcp://0.0.0.0:${TMKMS_PORT}\"|" \
    -e "s|^priv_validator_key_file *=|# priv_validator_key_file =|" \
    -e "s|^priv_validator_state_file *=|# priv_validator_state_file =|" \
    "$STATE_DIR/config/config.toml"
fi

set +e
echo "Key after TMKMS integration"
$APP_NAME tendermint show-validator
cat "$STATE_DIR/config/priv_validator_key.json"
cat "$STATE_DIR/data/priv_validator_state.json"
set -e

update_configs

echo "Init for cosmovisor"
cosmovisor init /usr/bin/inferenced || {
  echo "Cosmovisor failed, idling the container..."
  tail -f /dev/null
}

echo "Starting cosmovisor and the chain"
#cosmovisor run start || {
#  echo "Cosmovisor failed, idling the container..."
#  tail -f /dev/null
#}

cosmovisor run start &
COSMOVISOR_PID=$!

if [ "$GENESIS_RUN_STAGE" = "keygen" ]; then
    sleep 40
    echo "Querying validator pubkey, please write it down"

    echo "Querying validator pubkey, printing to log and saving to artifacts..."
    wget -qO - "http://localhost:26657/status" | tee /root/artifacts/validator_pubkey.json
    echo "$GENESIS_ADDRESS" > /root/artifacts/address.txt

    echo "Keygen stage is set, exiting. You can tear down the container now."
    exit 0
fi

if [ "$GENESIS_RUN_STAGE" != "start" ]; then
    echo "Expected GENESIS_RUN_STAGE to be 'start'. Exiting."
    exit 1
fi

sleep 40 # wait for the first block

# import private key for tgbot and sign tx to make tgbot public key registered n the network
if [ "$INIT_TGBOT" = "true" ]; then
    echo "Initializing tgbot account..."

    if [ -z "$TGBOT_PRIVATE_KEY_PASS" ]; then
        echo "Error: TGBOT_PRIVATE_KEY_PASS is empty. Aborting initialization."
        exit 1
    fi

    echo "$TGBOT_PRIVATE_KEY_PASS" | inferenced keys import tgbot tgbot_private_key.json

    inferenced tx bank send $TG_ACC $TG_ACC 100nicoin --from tgbot --yes
    echo "✅ tgbot account successfully initialized!"
else
    echo "INIT_TGBOT is not set to true. Skipping tgbot initialization."
fi

wait $COSMOVISOR_PID
