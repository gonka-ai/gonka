# Minimal Pre-Init Key Setup

```
inferenced query inference list-participant --node http://genesis-node:26657  --chain-id gonka-testnet-4
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



----

`node`:
❯ docker exec -it join1-node /bin/sh
~ # inferenced keys add operational-keys --keyring-dir .inference/

- address: gonka1m7r8uzf7wpc784kyaetdqw9zuguy9k44v5whva
  name: operational-keys
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"AlwABUIJmpS9EHCzXs6x+xRBimLLAhUvyqY6ZTk5RRKq"}'
  type: local

~ # inferenced tendermint show-validator
{"@type":"/cosmos.crypto.ed25519.PubKey","key":"rt+sDTuxGw94CVovABa6f4PqWclhZ0LjHLqKAEG/g/A="}

`outside`
~ # inferenced register-new-participant \
    gonka1ukeep7fwutyu826tuywjtryx8tkh88clvunydg \
    "http://join-1-api:8080" \
    "At7CoNxKqoeH96761IMR8p1sPNyOFdVjoMNREFGw3KGS" \
    "rt+sDTuxGw94CVovABa6f4PqWclhZ0LjHLqKAEG/g/A=" \
     --node-address http://genesis-api:9000


~ # inferenced keys list
Enter keyring passphrase (attempt 1/3):
password must be at least 8 characters
Enter keyring passphrase (attempt 2/3):
- address: gonka1ukeep7fwutyu826tuywjtryx8tkh88clvunydg
  name: account-key
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"At7CoNxKqoeH96761IMR8p1sPNyOFdVjoMNREFGw3KGS"}'
  type: local


inferenced tx inference grant-ml-ops-permissions \
    account-key gonka1m7r8uzf7wpc784kyaetdqw9zuguy9k44v5whva \
    --node http://genesis-node:26657 \
    --unordered --timeout-duration 1m \
    --from account-key \
    --chain-id gonka-testnet-5 \
    --gas auto

inferenced query tx 5B157B0A881F7A152C3A3B3C3FAFBBE443049B3A87355498DE2C6AC70C728F25 --node http://genesis-node:26657


# Instruction 

SEED_NODE_RPC=http://36.189.234.237:19252/chain-rpc/

## From local device
### 1. Create Cold Key
❯ ./inferenced keys add operator-19254 --keyring-backend test
override the existing name operator-19254 [y/N]: y

- address: gonka1sknqf7usat47vx6ljfyxj2uzkntqrjedf3rafv
  name: operator-19254
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"A90FVPMlZhWnvM5EYmAntmFnGgYYwX8XNNDVDbEny/p3"}'
  type: local

### 2. Run containers excluding `api`
```
docker compose -f docker-compose.mlnode.yml -f docker-compose.yml up  -d // api will fail
```

### 3. Create warm key
```
docker compose -f docker-compose.mlnode.yml -f docker-compose.yml run -it api /bin/sh
~ # inferenced keys add $KEY_NAME --keyring-backend test

- address: gonka19w64m5dahlg2s98sup94x4t0ydt0ng3wl6gykr
  name: node-702105
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"A6nltaY4mkTGp12tSwkvdKdqcGWDEIjeUT40i7jViiyI"}'
  type: local
```

### 3. Get Validator Public Key
```
curl -s http://36.189.234.237:19252/chain-rpc/status | jq -r '.result.validator_info.pub_key.value'
```

### 4. Register participant

register-new-participant [operator-address] [node-url] [operator-public-key] [validator-consensus-key]

```
./inferenced register-new-participant \
    gonka1lqchmm97zcs9khyt38kga8ftcdacjcq78kk5mq \
    "http://36.189.234.237:19254" \
    "AvxOXFyZXsrP7d0oGrSxMvv06AyUU8AqDCjGErDQvgBQ" \
    "x+OH2yt/GC/zK/fR5ImKnlfrmE6nZO/11FKXOpWRmAA=" \
    --node-address http://36.189.234.237:19250
```

### 5. Grant ML Permissions
gonka12fum536l6jyr6vwy7atsgfp07uwrtt9j48cd3q

./inferenced tx inference grant-ml-ops-permissions \
    operator-19254 gonka14mqkfa4j4d0mg6fzxpytqahxhkxlfgxaxgp8vc \
    --node http://36.189.234.237:19250/chain-rpc/ \
    --unordered --timeout-duration 1m \
    --from operator-19254 \
    --gas 2000000 \
    --keyring-backend test \
    --chain-id gonka-testnet-5