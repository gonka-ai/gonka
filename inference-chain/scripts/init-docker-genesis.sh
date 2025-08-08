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

export KEY_NAME="genesis"
export APP_NAME="inferenced"
export CHAIN_ID="gonka-testnet-7"
export COIN_DENOM="nicoin"
export STATE_DIR="/root/.inference"

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
  echo "Modified $json_file with file: $override_file"
  cat "$json_file" | filter_cw20_code
}

# Usage
modify_genesis_file 'denom.json'
MILLION_BASE="000000$COIN_DENOM"
NATIVE="000000000$COIN_DENOM"
MILLION_NATIVE="000000$NATIVE"

echo "Adding the keys to the genesis account"
$APP_NAME genesis add-genesis-account "$KEY_NAME" "122$MILLION_NATIVE" --keyring-backend $KEYRING_BACKEND
$APP_NAME genesis add-genesis-account "POOL_product_science_inc" "160$MILLION_NATIVE" --keyring-backend $KEYRING_BACKEND

$APP_NAME genesis gentx "$KEY_NAME" "1$MILLION_BASE" --chain-id "$CHAIN_ID" || {
  echo "Failed to create gentx"
  tail -f /dev/null
}
output=$($APP_NAME genesis collect-gentxs 2>&1)
echo "$output" | filter_cw20_code

# tgbot
TG_ACC=gonka1va4hlpg929n6hhg4wc8hl0g9yp4nheqxm6k9wr

if [ "$INIT_TGBOT" = "true" ]; then
  echo "Adding the tgbot account"
  $APP_NAME genesis add-genesis-account $TG_ACC "100$MILLION_NATIVE" --keyring-backend $KEYRING_BACKEND
fi

modify_genesis_file 'genesis_overrides.json'
modify_genesis_file "$HOME/.inference/genesis_overrides.json"
echo "Genesis file created"
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
sleep 20 # wait for the first block

# Deploy Liquidity Pool Contract at Genesis
deploy_liquidity_pool() {
  echo "Deploying Liquidity Pool Contract at Genesis..."
  
  if [ ! -d "/root/liquidity-pool" ]; then
    echo "Liquidity pool directory not found at /root/liquidity-pool"
    return
  fi
  
    cd /root/liquidity-pool
    
  # Build contract if WASM doesn't exist
  if [ ! -f "artifacts/liquidity_pool.wasm" ]; then
    echo "Building liquidity pool contract..."
    if [ -f "./build.sh" ]; then
      ./build.sh || echo "Failed to build contract"
    fi
  fi
  
  # Wait for chain to be ready before deploying
  echo "Waiting for chain to be ready..."
  for i in {1..30}; do
    if $APP_NAME query bank balances $($APP_NAME keys show "$KEY_NAME" -a --keyring-backend $KEYRING_BACKEND) >/dev/null 2>&1; then
      echo "Chain is ready"
      break
    fi
    echo "Waiting for chain... ($i/30)"
    sleep 2
  done
  
  echo "Funding community pool with 120M tokens for liquidity pool..."
  fund_output=$($APP_NAME tx distribution fund-community-pool "120000000000000000$COIN_DENOM" \
    --from "$KEY_NAME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --chain-id "$CHAIN_ID" \
    --gas auto \
    --gas-adjustment 1.3 \
    --fees "1000$COIN_DENOM" \
    --yes 2>&1)
  fund_status=$?

  if [ $fund_status -ne 0 ]; then
    echo "Community pool funding failed with exit code $fund_status."
    echo "See transaction output above for details."
    exit 1
  fi
    
  sleep 5  # Wait for transaction to be processed
    
  # Deploy liquidity pool contract
  echo "Step 1: Deploying liquidity pool contract..."
  if ! sh ./deploy.sh; then
    echo "Deploy failed, stopping workflow"
    return
  fi
  echo "Step 1: Deploy successful"
    
  echo "Step 2: Voting on contract proposal..."
  if ! sh ./vote.sh; then
    echo "Vote failed, stopping workflow"
    return
  fi
  echo "Step 2: Vote successful"
  
  echo "Step 3: Funding the contract..."
  if ! sh ./funding.sh; then
    echo "Funding failed, stopping workflow"
    return
  fi
  echo "Step 3: Funding successful"
  
  echo "Step 4: Voting on funding contract..."
  if ! sh ./vote.sh; then
    echo "Funding vote failed"
    return
  fi
  echo "Step 4: Funding vote successful"
}

# Deploy liquidity pool if enabled
if [ "${DEPLOY_LIQUIDITY_POOL:-}" = "true" ]; then
  deploy_liquidity_pool
else
  echo "Skipping liquidity pool deployment (DEPLOY_LIQUIDITY_POOL not set to true)"
fi

# import private key for tgbot and sign tx to make tgbot public key registered n the network
if [ "$INIT_TGBOT" = "true" ]; then
    echo "Initializing tgbot account..."

    if [ -z "$TGBOT_PRIVATE_KEY_PASS" ]; then
        echo "Error: TGBOT_PRIVATE_KEY_PASS is empty. Aborting initialization."
        exit 1
    fi

    echo "$TGBOT_PRIVATE_KEY_PASS" | inferenced keys import tgbot tgbot_private_key.json

    inferenced tx bank send $TG_ACC $TG_ACC 100nicoin --from tgbot --yes
    echo "tgbot account successfully initialized!"
else
    echo "INIT_TGBOT is not set to true. Skipping tgbot initialization."
fi

wait $COSMOVISOR_PID