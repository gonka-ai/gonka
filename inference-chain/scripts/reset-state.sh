#!/usr/bin/env sh
set -euo pipefail

APP_NAME="${APP_NAME:-inferenced}"
STATE_DIR="${STATE_DIR:-/root/.inference}"
RESET_COSMOVISOR="${RESET_COSMOVISOR:-true}"

command -v "$APP_NAME" >/dev/null ||
  { echo >&2 "ERR: $APP_NAME not in PATH"; exit 1; }

echo "Resetting Tendermint DB in $STATE_DIR"
"$APP_NAME" tendermint unsafe-reset-all --home "$STATE_DIR" --keep-addr-book

if [ "$RESET_COSMOVISOR" = "true" ]; then
  echo "Cleaning old Cosmovisor metadata"
  CV_DIR="$STATE_DIR/cosmovisor"
  rm -f  "$STATE_DIR/upgrade-info.json"
  rm -rf "$CV_DIR/genesis" "$CV_DIR/upgrades"
fi

SNAPSHOT_INTERVAL="${SNAPSHOT_INTERVAL:-1000}"
SNAPSHOT_KEEP_RECENT="${SNAPSHOT_KEEP_RECENT:-5}"
TRUSTED_BLOCK_PERIOD="${TRUSTED_BLOCK_PERIOD:-2000}"

echo "Configuring state-sync: interval=$SNAPSHOT_INTERVAL keep=$SNAPSHOT_KEEP_RECENT period=$TRUSTED_BLOCK_PERIOD"
"$APP_NAME" config set app state-sync.snapshot-interval      "$SNAPSHOT_INTERVAL"
"$APP_NAME" config set app state-sync.snapshot-keep-recent   "$SNAPSHOT_KEEP_RECENT"
"$APP_NAME" set-statesync               "$STATE_DIR/config/config.toml" true
"$APP_NAME" set-statesync-rpc-servers   "$STATE_DIR/config/config.toml" "$RPC_SERVER_URL_1" "$RPC_SERVER_URL_2"
"$APP_NAME" set-statesync-trusted-block "$STATE_DIR/config/config.toml" "$SEED_NODE_RPC_URL" "$TRUSTED_BLOCK_PERIOD"