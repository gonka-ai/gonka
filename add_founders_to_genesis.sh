#!/bin/bash

# Script to add founders' vesting accounts to genesis using inferenced CLI
# Usage: ./add_founders_to_genesis.sh

set -e  # Exit on any error

# Configuration
FOUNDERS_LEDGER="genesis/founders_ledger.json"
INFERENCED_PATH="./inference-chain/build/inferenced"
GENESIS_HOME="./genesis/"
GENESIS_DRAFT_PATH="./genesis/genesis-draft.json"

# Check if draft exists
if [ ! -f "$GENESIS_DRAFT_PATH" ]; then
    echo "Error: genesis-draft.json not found at $GENESIS_DRAFT_PATH"
    exit 1
fi

echo $"Copy draft to genesis"
cp $GENESIS_DRAFT_PATH $GENESIS_HOME/config/genesis.json

# Check if inferenced binary exists
if [ ! -f "$INFERENCED_PATH" ]; then
    echo "Error: inferenced binary not found at $INFERENCED_PATH"
    echo "Please build the binary first or adjust the INFERENCED_PATH variable"
    exit 1
fi

# Check if founders ledger exists
if [ ! -f "$FOUNDERS_LEDGER" ]; then
    echo "Error: founders ledger not found at $FOUNDERS_LEDGER"
    exit 1
fi

# Check if genesis config exists
if [ ! -f "$GENESIS_HOME/config/genesis.json" ]; then
    echo "Error: genesis.json not found at $GENESIS_HOME/config/genesis.json"
    echo "Please make sure you've copied genesis-draft.json to genesis/genesis-draft.json"
    exit 1
fi

# Calculate vesting times
# Start time: 20:20 PST 08/20/2025 = 04:20 UTC 08/21/2025
START_TIME=$(date -u -j -f "%Y-%m-%d %H:%M:%S" "2025-08-21 04:20:00" +%s)
# End time: 4 years later
END_TIME=$((START_TIME + 4 * 365 * 24 * 60 * 60))

echo "Adding founders' vesting accounts to genesis..."
echo "Vesting start time: $(date -u -r $START_TIME '+%Y-%m-%d %H:%M:%S UTC') (20:20 PST 08/20/2025)"
echo "Vesting end time: $(date -u -r $END_TIME '+%Y-%m-%d %H:%M:%S UTC')"
echo "Genesis home: $GENESIS_HOME"
echo ""

# Parse founders ledger and add accounts
account_count=0
total_amount=0

while IFS=$'\t' read -r address amount_str || [ -n "$address" ]; do
    # Skip empty lines
    if [ -z "$address" ] || [ -z "$amount_str" ]; then
        continue
    fi
    
    # Clean up the amount (remove commas)
    amount=$(echo "$amount_str" | tr -d ',' | tr -d ' ')
    
    # Validate address format
    if [[ ! "$address" =~ ^gonka1[a-zA-Z0-9]{38,}$ ]]; then
        echo "Warning: Skipping invalid address: $address"
        continue
    fi
    
    # Validate amount is numeric
    if ! [[ "$amount" =~ ^[0-9]+$ ]]; then
        echo "Warning: Skipping invalid amount for $address: $amount_str"
        continue
    fi
    
    echo "Adding account: $address with ${amount}ngonka"
    
    # Add the vesting account using inferenced CLI
    if $INFERENCED_PATH genesis add-genesis-account "$address" "${amount}ngonka" \
        --vesting-amount "${amount}ngonka" \
        --vesting-start-time "$START_TIME" \
        --vesting-end-time "$END_TIME" \
        --home "$GENESIS_HOME"; then
        
        account_count=$((account_count + 1))
        total_amount=$((total_amount + amount))
        echo "✓ Successfully added $address"
    else
        echo "✗ Failed to add $address"
    fi
    
done < "$FOUNDERS_LEDGER"

echo ""
echo "=== SUMMARY ==="
echo "Successfully added: $account_count accounts"
echo "Total vesting amount: $(printf "%'d" $total_amount) ngonka"
echo "Genesis file updated at: $GENESIS_HOME/config/genesis.json"
echo ""
echo "Vesting schedule:"
echo "  Start: $(date -u -r $START_TIME '+%Y-%m-%d %H:%M:%S UTC') (20:20 PST 08/20/2025)"
echo "  End: $(date -u -r $END_TIME '+%Y-%m-%d %H:%M:%S UTC')"
echo "  Duration: 4 years"
