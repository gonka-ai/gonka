# Minimal Pre-Init Key Setup

```
inferenced query inference list-participant --limit 50 --node http://genesis-node:26657  --chain-id gonka-testnet-4
```

Copy-paste these commands to test the two-key system:

## 1. Create Keys
```bash
# Create operator key (cold wallet)
inferenced keys add operator --keyring-backend file

# Create AI ops key (hot wallet) 
inferenced keys add ml-ops --keyring-backend file

# Get addresses
AI_OPS_ADDR=$(inferenced keys show genesis-ai-ops -a --keyring-backend file)
OPERATOR_ADDR=$(inferenced keys show genesis-operator -a --keyring-backend file)
echo "AI Ops: $AI_OPS_ADDR"
echo "Operator: $OPERATOR_ADDR"
```

## 2. Grant Core Permissions (No Account Creation Needed!)
```bash
# Grant essential AI operation permissions directly
# Accounts are created automatically when authz grants are processed
inferenced tx authz grant $AI_OPS_ADDR generic --msg-type /inference.inference.MsgStartInference --from genesis-operator --keyring-backend file --yes --node http://genesis-node:26657  --chain-id gonka-testnet-4
inferenced tx authz grant $AI_OPS_ADDR generic --msg-type /inference.inference.MsgFinishInference --from genesis-operator --keyring-backend file --yes --node http://genesis-node:26657  --chain-id gonka-testnet-4
inferenced tx authz grant $AI_OPS_ADDR generic --msg-type /inference.inference.MsgClaimRewards --from genesis-operator --keyring-backend file --yes --node http://genesis-node:26657  --chain-id gonka-testnet-4
```

## 3. Test Permission
```bash
# Check if permission granted
inferenced query authz grants $OPERATOR_ADDR $AI_OPS_ADDR --node http://genesis-node:26657 --chain-id gonka-testnet-4

# Or check specific message type
inferenced query authz grants $OPERATOR_ADDR $AI_OPS_ADDR /inference.inference.MsgStartInference --node http://genesis-node:26657 --chain-id gonka-testnet-4
```

## 4. Export Key for Server
```bash
# Export AI ops key for server use (outputs to stdout, redirect to file)
inferenced keys export genesis-ai-ops --keyring-backend file > ai-ops.json
```

Done! Now AI ops key can execute operations via authz on behalf of operator key without any account pre-creation.

**Key Insight:** Cosmos SDK accounts exist as addresses before public keys are revealed. The public key is only stored on-chain when the first transaction is signed by that key. 