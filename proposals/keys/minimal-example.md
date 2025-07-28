# Minimal Pre-Init Key Setup

Copy-paste these commands to test the two-key system:

## 1. Create Keys
```bash
# Create operator key (cold wallet)
inferenced keys add genesis-operator --keyring-backend file

# Create AI ops key (hot wallet) 
inferenced keys add genesis-ai-ops --keyring-backend file

# Get addresses
AI_OPS_ADDR=$(inferenced keys show genesis-ai-ops -a --keyring-backend file)
OPERATOR_ADDR=$(inferenced keys show genesis-operator -a --keyring-backend file)
echo "AI Ops: $AI_OPS_ADDR"
echo "Operator: $OPERATOR_ADDR"
```

## 2. Fund AI Ops Account
```bash
# Send some tokens to AI ops account to create it on-chain
inferenced tx bank send genesis-operator $AI_OPS_ADDR 1nicoin --keyring-backend file --yes --node http://genesis-node:26657 --chain-id gonka-testnet-4

# Wait for transaction to be processed
sleep 5
```

## 3. Grant Core Permissions
```bash
# Grant essential AI operation permissions
inferenced tx authz grant $AI_OPS_ADDR generic --msg-type /inference.inference.MsgStartInference --from genesis-operator --keyring-backend file --yes --node http://genesis-node:26657  --chain-id gonka-testnet-4
inferenced tx authz grant $AI_OPS_ADDR generic --msg-type /inference.inference.MsgFinishInference --from genesis-operator --keyring-backend file --yes --node http://genesis-node:26657  --chain-id gonka-testnet-4
inferenced tx authz grant $AI_OPS_ADDR generic --msg-type /inference.inference.MsgClaimRewards --from genesis-operator --keyring-backend file --yes --node http://genesis-node:26657  --chain-id gonka-testnet-4
```

## 4. Test Permission
```bash
# Check if permission granted
inferenced query authz grants $OPERATOR_ADDR $AI_OPS_ADDR --node http://genesis-node:26657 --chain-id gonka-testnet-4

# Or check specific message type
inferenced query authz grants $OPERATOR_ADDR $AI_OPS_ADDR /inference.inference.MsgStartInference --node http://genesis-node:26657 --chain-id gonka-testnet-4
```

## 5. Export Key for Server
```bash
# Export AI ops key for server use (outputs to stdout, redirect to file)
inferenced keys export genesis-ai-ops --keyring-backend file > ai-ops.json
```

Done! Now AI ops key can execute operations via authz on behalf of operator key. 