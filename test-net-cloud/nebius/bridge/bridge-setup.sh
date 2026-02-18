#!/bin/bash
set -e

# Default Genesis Host
GENESIS_HOST="${GENESIS_HOST:-89.169.111.79}"
# Relative path to the bridge contract directory
BRIDGE_DIR="../../proposals/ethereum-bridge-contact"
ENV_FILE="$BRIDGE_DIR/.env"
EXAMPLE_FILE="$BRIDGE_DIR/.env.example"

# Ensure we are in the correct directory
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$DIR"

echo "Setting up environment for Bridge Contract in $BRIDGE_DIR..."

echo "Using Genesis Host: $GENESIS_HOST"

# Check if .env.example exists
if [ ! -f "$EXAMPLE_FILE" ]; then
    echo "Error: $EXAMPLE_FILE not found!"
    exit 1
fi

# Copy .env.example to .env if .env doesn't exist
if [ ! -f "$ENV_FILE" ]; then
    echo "Creating .env from $EXAMPLE_FILE..."
    cp "$EXAMPLE_FILE" "$ENV_FILE"
else
    echo ".env already exists. Updating values..."
fi

# Fetch current epoch
echo "Fetching current epoch..."
EPOCH=$(curl -s "http://$GENESIS_HOST:8000/chain-api/productscience/inference/inference/get_current_epoch" | jq -r .epoch)

if [ -z "$EPOCH" ] || [ "$EPOCH" == "null" ]; then
    echo "Error: Failed to fetch current epoch from $GENESIS_HOST"
    exit 1
fi
echo "Current Epoch: $EPOCH"

# Fetch Group Public Key
echo "Fetching Group Public Key..."
GROUP_KEY_B64=$(curl -s "http://$GENESIS_HOST:8000/chain-api/productscience/inference/bls/epoch_data/$EPOCH" | jq -r .epoch_data.group_public_key)

if [ -z "$GROUP_KEY_B64" ] || [ "$GROUP_KEY_B64" == "null" ]; then
    echo "Error: Failed to fetch group public key"
    exit 1
fi

# Convert Base64 to Hex
# Decode base64 -> convert to hex -> ensure it's on one line
GROUP_KEY_HEX="0x$(echo "$GROUP_KEY_B64" | base64 -d | xxd -p -c 1000)"
echo "Group Public Key (Hex): $GROUP_KEY_HEX"

# Update .env file
# We use a temporary file to handle cross-platform sed differences safely or just logical replacement
if grep -q "GENESIS_GROUP_PUBLIC_KEY=" "$ENV_FILE"; then
    # Replace existing line
    # Escape special chars in key if any (though hex is safe)
    sed -i.bak "s|GENESIS_GROUP_PUBLIC_KEY=.*|GENESIS_GROUP_PUBLIC_KEY=$GROUP_KEY_HEX|" "$ENV_FILE"
    rm "$ENV_FILE.bak"
else
    # Append if not found
    echo "GENESIS_GROUP_PUBLIC_KEY=$GROUP_KEY_HEX" >> "$ENV_FILE"
fi

if grep -q "GENESIS_HOST=" "$ENV_FILE"; then
    sed -i.bak "s|GENESIS_HOST=.*|GENESIS_HOST=$GENESIS_HOST|" "$ENV_FILE"
    rm "$ENV_FILE.bak"
else
    echo "GENESIS_HOST=$GENESIS_HOST" >> "$ENV_FILE"
fi

# Set SEPOLIA_RPC_URL for testnet setup
SEPOLIA_RPC="https://ethereum-sepolia-rpc.publicnode.com"
if grep -q "SEPOLIA_RPC_URL=" "$ENV_FILE"; then
    if grep -q "SEPOLIA_RPC_URL=$" "$ENV_FILE" || grep -q "SEPOLIA_RPC_URL=\"\"" "$ENV_FILE"; then
         # Only update if empty
         sed -i.bak "s|SEPOLIA_RPC_URL=.*|SEPOLIA_RPC_URL=$SEPOLIA_RPC|" "$ENV_FILE"
         rm "$ENV_FILE.bak"
         echo "Updated SEPOLIA_RPC_URL to $SEPOLIA_RPC"
    fi
else
    echo "SEPOLIA_RPC_URL=$SEPOLIA_RPC" >> "$ENV_FILE"
    echo "Added SEPOLIA_RPC_URL=$SEPOLIA_RPC"
fi

# Handle Private Key argument
PRIVATE_KEY="$1"

if [ -n "$PRIVATE_KEY" ]; then
    echo "Updating PRIVATE_KEY in .env..."
    if grep -q "PRIVATE_KEY=" "$ENV_FILE"; then
         # Use a different delimiter to avoid issues with slashes in key (though rare in hex/base64)
         # Using | as delimiter
         sed -i.bak "s|PRIVATE_KEY=.*|PRIVATE_KEY=$PRIVATE_KEY|" "$ENV_FILE"
         rm "$ENV_FILE.bak"
    else
         echo "PRIVATE_KEY=$PRIVATE_KEY" >> "$ENV_FILE"
    fi
    echo "Private key updated."
else
    echo "No private key provided as argument."
    echo "Checking if PRIVATE_KEY is already set in .env..."
    if grep -q "PRIVATE_KEY=" "$ENV_FILE" && ! grep -q "PRIVATE_KEY=$" "$ENV_FILE"; then
        echo "PRIVATE_KEY found in .env, proceeding..."
    else
        echo "WARNING: PRIVATE_KEY is missing or empty in .env. Deployment may fail."
        echo "Usage: ./bridge-setup.sh <PRIVATE_KEY>"
    fi
fi

echo "Environment setup complete! Values updated in $ENV_FILE"

echo "Deploying Bridge Contract to Sepolia..."
cd "$BRIDGE_DIR"
# Ensure dependencies are installed (fast check)
if [ ! -d "node_modules" ]; then
    echo "Installing dependencies..."
    npm install
fi

echo "Running: npx hardhat run deploy.js --network sepolia"
OUTPUT=$(npx hardhat run deploy.js --network sepolia)
EXIT_CODE=$?

echo "$OUTPUT"

if [ $EXIT_CODE -eq 0 ]; then
    echo ""
    echo "Deployment Successful!"

    # Extract Bridge Contract Address from output
    BRIDGE_ADDRESS=$(echo "$OUTPUT" | grep "BridgeContract deployed to:" | awk '{print $NF}')
    
    if [ -n "$BRIDGE_ADDRESS" ]; then
        echo "=================================================="
        echo "BRIDGE CONTRACT ADDRESS: $BRIDGE_ADDRESS"
        echo "=================================================="
        # Optional: Save address to a file since .env is being deleted
        echo "$BRIDGE_ADDRESS" > "bridge_address.txt"
        echo "Address saved to: ${BRIDGE_DIR}/bridge_address.txt"
    else
         echo "WARNING: Could not parse Bridge Contract Address from output."
    fi

    echo "Security: Removing .env file..."
    rm "$ENV_FILE"
    echo ".env file removed."
else
    echo "Deployment Failed."
    exit $EXIT_CODE
fi
