#!/usr/bin/env bash

rm -rf $HOME/.inference
INFERENCED_BIN=$(which inferenced)
if [ -z "$INFERENCED_BIN" ]; then
    GOBIN=$(go env GOPATH)/bin
    INFERENCED_BIN=$(which $GOBIN/inferenced)
fi

if [ -z "$INFERENCED_BIN" ]; then
    echo "please verify inferenced is installed"
    exit 1
fi

DENOM="icoin"

# configure inferenced
$INFERENCED_BIN config set client chain-id demo
$INFERENCED_BIN config set client keyring-backend test
$INFERENCED_BIN keys add alice
$INFERENCED_BIN keys add bob
$INFERENCED_BIN init test --chain-id demo --default-denom "$DENOM"
$INFERENCED_BIN config set app minimum-gas-prices "0$DENOM"
# update genesis
$INFERENCED_BIN genesis add-genesis-account alice 10000000$DENOM --keyring-backend test
$INFERENCED_BIN genesis add-genesis-account bob 1000$DENOM --keyring-backend test
# create default validator
$INFERENCED_BIN genesis gentx alice 1000000$DENOM --chain-id demo
$INFERENCED_BIN genesis collect-gentxs
