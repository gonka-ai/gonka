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
  rm -rf "$CV_DIR"
fi