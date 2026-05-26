#!/bin/bash
set -e

# bridge-fund-account.sh
# Funds a specific account from the genesis cold wallet.

export BASE_DIR="${TESTNET_BASE_DIR:-/home/ubuntu}"
export KEY_DIR="$BASE_DIR/.inference"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"
export CHAIN_ID="gonka-testnet"
export APP_NAME="/srv/dai/inferenced" # Default for server, but we'll check

if [ ! -f "$APP_NAME" ]; then
    APP_NAME="/home/ubuntu/inferenced"
fi
if [ ! -f "$APP_NAME" ]; then
    APP_NAME="inferenced"
fi

RECIPIENT=""
AMOUNT="1000000000000000000" # Default 1 GNK (assuming 18 decimals)
PASSWORD="12345678"
USE_SUDO=false
NODE_OPTS="--node http://localhost:8000/chain-rpc/"

while [[ $# -gt 0 ]]; do
  case $1 in
    --to) RECIPIENT="$2"; shift 2 ;;
    --amount) AMOUNT="$2"; shift 2 ;;
    --password) PASSWORD="$2"; shift 2 ;;
    --sudo) USE_SUDO=true; shift ;;
    --node) NODE_OPTS="--node $2"; shift 2 ;;
    *) echo "Unknown option $1"; exit 1 ;;
  esac
done

if [ -z "$RECIPIENT" ]; then
    echo "Usage: ./bridge-fund-account.sh --to <GONKA_ADDR> [--amount <AMT_NGONKA>] [--sudo] [--node <URL>]"
    exit 1
fi

SUDO_CMD=""
if [ "$USE_SUDO" = "true" ]; then
    SUDO_CMD="sudo"
fi

echo "Funding account $RECIPIENT with $AMOUNT ngonka..."

printf "%s\n" "$PASSWORD" | $SUDO_CMD $APP_NAME tx bank send "$KEY_NAME" "$RECIPIENT" "${AMOUNT}ngonka" \
  --chain-id "$CHAIN_ID" \
  --home "$KEY_DIR" \
  --keyring-backend file \
  --gas auto --gas-adjustment 1.5 --yes $NODE_OPTS
