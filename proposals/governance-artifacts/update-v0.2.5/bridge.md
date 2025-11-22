## Gonka v0.2.5 — Exchange Bridge and Wrapped Tokens

### Introduction

Gonka v0.2.5 introduces an exchange bridge that connects external chains to the Gonka chain and a wrapped-token model for representing external assets on-chain. In the current realization, initial support starts with Ethereum (Prysm beacon + Geth execution), with additional chains planned. This design keeps the Ethereum clients simple, reduces risk by only handling finalized data, and lets governance centrally control which external contracts are accepted.

- Bridge
  - Off-chain bridge service runs Prysm (beacon) and Geth (execution) in ingestion-only mode. We do not participate in consensus or block validation; the node runs in ingestion-only mode, subscribing only to finalized blocks (≥2 epochs on Ethereum) to extract and filter relevant transactions.
  - The service retrieves trusted bridge contract addresses from the decentralized API - `GET /v1/bridge/addresses?chain=<name>` - per chain and filters receipts to only those addresses and events that matter.
  - For each finalized block, the adapter builds a minimal payload containing only matching receipts (if any) and posts it to the decentralized API - `POST /admin/v1/bridge/block` - empty payloads are still sent to maintain continuity and block tracking.
  - The reverse flow is supported via BLS-backed requests: users escrow GNK on Gonka to mint WGNK on a target external chain, and wrapped tokens on Gonka can initiate withdrawals back to external chains.

- Decentralized API
  - Enqueues and processes receipts, submitting `MsgBridgeExchange` signed by the API’s validator account.
  - Derives the Cosmos recipient address from the external tx signature public key (`OwnerPubKey → bech32`), and sets it as `OwnerAddress` in `MsgBridgeExchange` for automatic delivery to the sender’s on‑chain address.

- Chain node validation and minting
  - First `MsgBridgeExchange` for a receipt `(originChain, blockNumber, receiptIndex)` creates a pending `BridgeTransaction` bound to the current epoch and records the validator’s voting power.
  - Subsequent messages from other validators add their voting power. Mint/release occurs at strict majority: requiredPower = floor(totalEpochPower/2) + 1.
  - When majority is reached, `handleCompletedBridgeTransaction` executes:
    - If `contractAddress` matches a registered bridge contract on that chain (WGNK burn on external), native GNK is released from the bridge escrow to the recipient.
    - Otherwise (external token inbound), the chain mints W(EXT):
      - If a wrapped CW20 already exists for `(chainId, contractAddress)`, mint to the recipient.
      - If not, instantiate a new wrapped CW20 using the stored `wrapped_token_code_id`, with the inference module as minter and governance as admin; then mint to the recipient.

- Token metadata
  - Stored on-chain via governance: `MsgRegisterTokenMetadata(chainId, contractAddress, name, symbol, decimals, overwrite)`.
  - Not required at contract instantiation; if metadata exists, the chain updates the wrapped contract immediately after creation.
  - Metadata can be added or updated later (set `overwrite=true`), and the chain pushes updates to the existing wrapped contract.


```text
┌────────────────────────────────────────────────────────┐
│  Bridge Service (Off-chain Ethereum Adapter)           │
│  ────────────────────────────────────────────────────  │
│  • Runs Prysm (beacon) + Geth (execution) in           │
│    ingestion-only mode — no consensus or validation    │
│  • Subscribes to finalized blocks (≥2 epochs)          │
│  • Fetches trusted bridge contracts via                │
│    GET /v1/bridge/addresses?chain=ethereum             │
│  • Filters receipts → builds minimal payload (empty OK)│
│  • POSTs to API: /admin/v1/bridge/block                │
└───────────────────────────┬────────────────────────────┘
                            │
                            ▼
┌────────────────────────────────────────────────────────┐
│  Decentralized API                                     │
│  ────────────────────────────────────────────────────  │
│  • Queues payloads                                     │
│  • Submits MsgBridgeExchange to chain                  │
│  • Signed by validator key                             │
│  • Maps external sender → Cosmos recipient             │
└───────────────────────────┬────────────────────────────┘
                            │
                            ▼
┌────────────────────────────────────────────────────────┐
│  Gonka Chain Node (on-chain)                           │
│  ────────────────────────────────────────────────────  │
│  • Validates MsgBridgeExchange                         │
│  • Creates BridgeTransaction                           │
│      ├─ Status: PENDING  (<50% validator power)        │
│      └─ Status: COMPLETED (≥50% validator power)       │
└───────────────────────────┬────────────────────────────┘
                            │
                ┌───────────┴───────────┐
                ▼                       ▼
┌──────────────────────────┐  ┌──────────────────────────┐
│ Inbound (EXT→GNK)        │  │ Outbound (GNK→EXT)       │
│ ──────────────────────── │  │ ──────────────────────── │
│ • Mint W(EXT)            │  │ • Burn WGNK / Release GNK│
└───────────────┬──────────┘  └─────────┬────────────────┘
                │                       │
                ▼                       ▼
┌──────────────────────────┐   ┌──────────────────────────┐
│ Wrapped Token Handling   │   │ External Chain Settlement│
│ ───────────────────────  │   │ ───────────────────────  │
│ • If wrapped CW20 for    │   │ • BLS-signed withdrawal  │
│   (chainId,contract)     │   │   or mint confirmation   │
│   exists → mint tokens   │   │ • Completes external tx  │
│ • If not → create new    │   └──────────────────────────┘
│   wrapped contract:      │
│    ├─ Using existing     │
│    │  metadata (name,    │
│    │  symbol, decimals)  │
│    └─ Or blank metadata  │
│       if not registered  │
└──────────────────────────┘
```

### Governance Proposals \ Contract Deployment

After a successful version upgrade, perform these governance and operational steps on the live network:

- Liquidity Pool & Wrapped‑token contracts deploy
  Instruction:
    ```bash
    APP_NAME=${APP_NAME:-inferenced}
    CHAIN_ID=${CHAIN_ID:-gonka-mainnet}
    KEY_NAME=${KEY_NAME:-node1}
    KEYRING_BACKEND=${KEYRING_BACKEND:-file}
    COIN_DENOM=${COIN_DENOM:-ngonka}
    HOME_DIR=${HOME_DIR:-~/mainnet}
    NODE_RPC=${NODE_RPC:-http://node2.gonka.ai:8000/chain-rpc/}

    GOV_MODULE_ADDR=$($APP_NAME query auth module-accounts --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.accounts[] | select(.value.name=="gov") | .value.address')
    MIN_DEPOSIT_AMOUNT=$($APP_NAME query gov params --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.params.min_deposit[] | select(.denom=="'"$COIN_DENOM"'") | .amount')

    LP_WASM="inference-chain/contracts/liquidity-pool/artifacts/liquidity_pool.wasm"
    echo "Storing liquidity-pool wasm..."
    LP_STORE_TX=$($APP_NAME tx wasm store "$LP_WASM" \
      --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" \
      --chain-id "$CHAIN_ID" --node "$NODE_RPC" --gas auto --gas-adjustment 1.3 \
      --broadcast-mode sync --output json --yes)
    LP_TX_HASH=$(echo "$LP_STORE_TX" | jq -r '.txhash')
    for i in $(seq 1 30); do
      TX_QUERY=$($APP_NAME query tx "$LP_TX_HASH" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json 2>/dev/null || echo "")
      LP_CODE_ID=$(echo "$TX_QUERY" | jq -r '.events[] | select(.type=="store_code") | .attributes[] | select(.key=="code_id") | .value' | head -n1)
      [ -n "$LP_CODE_ID" ] && break; sleep 2; done
    [ -z "$LP_CODE_ID" ] && echo "Error: liquidity-pool code_id not found" && exit 1
    echo "Liquidity-pool contract LP_CODE_ID=$LP_CODE_ID"
  
    WT_WASM="inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm"
    echo "Storing wrapped-token wasm..."
    STORE_TX=$($APP_NAME tx wasm store "$WT_WASM" \
      --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" \
      --chain-id "$CHAIN_ID" --node "$NODE_RPC" --gas auto --gas-adjustment 1.3 \
      --broadcast-mode sync --output json --yes)
    TX_HASH=$(echo "$STORE_TX" | jq -r '.txhash')
    for i in $(seq 1 30); do
      TX_QUERY=$($APP_NAME query tx "$TX_HASH" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json 2>/dev/null || echo "")
      WT_CODE_ID=$(echo "$TX_QUERY" | jq -r '.events[] | select(.type=="store_code") | .attributes[] | select(.key=="code_id") | .value' | head -n1)
      [ -n "$WT_CODE_ID" ] && break; sleep 2; done
    [ -z "$WT_CODE_ID" ] && echo "Error: code_id not found" && exit 1
    echo "Wrapped-token contract WT_CODE_ID=$WT_CODE_ID"
    ```


- Global initialization proposal (one vote)
  - Register wrapped-token code ID via `MsgRegisterWrappedTokenContract`
  - Register Liquidity Pool via `MsgRegisterLiquidityPool`
  - Register bridge addresses via `MsgRegisterBridgeAddresses`
  - Register metadata for USDC and USDT via `MsgRegisterTokenMetadata`
  - Approve USDC and USDT for trading via `MsgApproveBridgeTokenForTrading`
  
  - Note: Upload WASM first to obtain `WT_CODE_ID` and `LP_CODE_ID`. Liquidity Pool funding remains a separate proposal.

  Instruction:
    ```bash
    APP_NAME=${APP_NAME:-inferenced}
    CHAIN_ID=${CHAIN_ID:-gonka-mainnet}
    KEY_NAME=${KEY_NAME:-node1}
    KEYRING_BACKEND=${KEYRING_BACKEND:-file}
    COIN_DENOM=${COIN_DENOM:-ngonka}
    HOME_DIR=${HOME_DIR:-~/mainnet}
    NODE_RPC=${NODE_RPC:-http://node2.gonka.ai:8000/chain-rpc/}

    GOV_MODULE_ADDR=$($APP_NAME query auth module-accounts --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.accounts[] | select(.value.name=="gov") | .value.address')
    MIN_DEPOSIT_AMOUNT=$($APP_NAME query gov params --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.params.min_deposit[] | select(.denom=="'"$COIN_DENOM"'") | .amount')

    # REQUIRED: set code IDs obtained from previous wasm store commands
    WT_CODE_ID=<WRAPPED_TOKEN_CODE_ID>      # replace with value printed earlier
    LP_CODE_ID=<LIQUIDITY_POOL_CODE_ID>  # replace with value printed earlier

    # Liquidity Pool instantiate config
    LP_LABEL="liquidity-pool"
    LP_INSTANTIATE_MSG=$(jq -n --arg admin "$GOV_MODULE_ADDR" '{admin:$admin, daily_limit_bp:"1000", total_supply:"120000000000000000000"}')

    # Bridge addresses to whitelist
    CHAIN_NAME="ethereum"
    ADDRESSES_JSON='["0xYourBridgeContract1","0xYourBridgeContract2"]' # replace with actual bridge contracts

    # USDC
    USDC_CONTRACT="0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
    USDC_NAME="USD Coin"
    USDC_SYMBOL="USDC"
    USDC_DECIMALS=6

    # USDT
    USDT_CONTRACT="0xdac17f958d2ee523a2206206994597c13d831ec7"
    USDT_NAME="Tether USD"
    USDT_SYMBOL="USDT"
    USDT_DECIMALS=6

    cat > proposal_global_init.json <<EOF
    {
      "messages": [
        {
          "@type": "/inference.inference.MsgRegisterWrappedTokenContract",
          "authority": "$GOV_MODULE_ADDR",
          "codeId": $WT_CODE_ID
        },
        {
          "@type": "/inference.inference.MsgRegisterLiquidityPool",
          "authority": "$GOV_MODULE_ADDR",
          "codeId": $LP_CODE_ID,
          "label": "$LP_LABEL",
          "instantiateMsg": $LP_INSTANTIATE_MSG
        },
        {
          "@type": "/inference.inference.MsgRegisterBridgeAddresses",
          "authority": "$GOV_MODULE_ADDR",
          "chainName": "$CHAIN_NAME",
          "addresses": $(echo $ADDRESSES_JSON)
        },
        {
          "@type": "/inference.inference.MsgRegisterTokenMetadata",
          "authority": "$GOV_MODULE_ADDR",
          "chainId": "$CHAIN_NAME",
          "contractAddress": "$USDC_CONTRACT",
          "name": "$USDC_NAME",
          "symbol": "$USDC_SYMBOL",
          "decimals": $USDC_DECIMALS,
          "overwrite": false
        },
        {
          "@type": "/inference.inference.MsgApproveBridgeTokenForTrading",
          "authority": "$GOV_MODULE_ADDR",
          "chainId": "$CHAIN_NAME",
          "contractAddress": "$USDC_CONTRACT"
        },
        {
          "@type": "/inference.inference.MsgRegisterTokenMetadata",
          "authority": "$GOV_MODULE_ADDR",
          "chainId": "$CHAIN_NAME",
          "contractAddress": "$USDT_CONTRACT",
          "name": "$USDT_NAME",
          "symbol": "$USDT_SYMBOL",
          "decimals": $USDT_DECIMALS,
          "overwrite": false
        },
        {
          "@type": "/inference.inference.MsgApproveBridgeTokenForTrading",
          "authority": "$GOV_MODULE_ADDR",
          "chainId": "$CHAIN_NAME",
          "contractAddress": "$USDT_CONTRACT"
        }
      ],
      "deposit": "${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}",
      "title": "Global Init: Wrapped code, LP, bridge addresses, USDC & USDT",
      "summary": "Register wrapped-token code ID, instantiate Liqudity Pool, whitelist bridge addresses, add metadata and enable trading for USDC and USDT",
      "metadata": "https://github.com/gonka-ai/gonka/blob/main/proposals/bridge_smart_contract/README.md"
    }
    EOF

    echo "Submitting global initialization proposal..."
    GLOBAL_PROP_TX=$($APP_NAME tx gov submit-proposal proposal_global_init.json \
      --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --chain-id "$CHAIN_ID" --node "$NODE_RPC" \
      --gas auto --gas-adjustment 1.3 --broadcast-mode sync --output json --yes)
    GLOBAL_PROP_HASH=$(echo "$GLOBAL_PROP_TX" | jq -r '.txhash')
    GLOBAL_PROP_ID=$($APP_NAME query tx "$GLOBAL_PROP_HASH" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json 2>/dev/null \
      | jq -r '.events[] | select(.type=="submit_proposal") | .attributes[] | select(.key=="proposal_id") | .value' | head -n1)
    echo "Voting YES on proposal $GLOBAL_PROP_ID ..."
    $APP_NAME tx gov vote "$GLOBAL_PROP_ID" yes --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --gas auto --yes
    ```

- Fund Liquidity Pool
  - After LP registration passes (from either separate LP proposal or the Global initialization proposal), submit a community-pool spend to fund the LP. See the "Fund Liquidity Pool" section below.
  
  Instruction:
    ```bash
    APP_NAME=${APP_NAME:-inferenced}
    CHAIN_ID=${CHAIN_ID:-gonka-mainnet}
    KEY_NAME=${KEY_NAME:-node1}
    KEYRING_BACKEND=${KEYRING_BACKEND:-file}
    COIN_DENOM=${COIN_DENOM:-ngonka}
    HOME_DIR=${HOME_DIR:-~/mainnet}
    NODE_RPC=${NODE_RPC:-http://node2.gonka.ai:8000/chain-rpc/}

    GOV_MODULE_ADDR=$($APP_NAME query auth module-accounts --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.accounts[] | select(.value.name=="gov") | .value.address')
    MIN_DEPOSIT_AMOUNT=$($APP_NAME query gov params --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.params.min_deposit[] | select(.denom=="'"$COIN_DENOM"'") | .amount')

    echo "Getting LP address after deployment passed..."
    LP_ADDR=$($APP_NAME query inference liquidity-pool --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json 2>/dev/null | jq -r '.address')
    echo "LP_ADDR=$LP_ADDR"

    echo "Submitting funding proposal..."
    FUND_AMOUNT="1" # adjust to your token base units
    cat > proposal_fund_lp.json <<EOF
    {
      "messages": [{
        "@type": "/cosmos.distribution.v1beta1.MsgCommunityPoolSpend",
        "authority": "$GOV_MODULE_ADDR",
        "recipient": "$LP_ADDR",
        "amount": [{"denom": "$COIN_DENOM", "amount": "$FUND_AMOUNT"}]
      }],
      "deposit": "${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}",
      "title": "Fund Liquidity Pool",
      "summary": "Allocate tokens to LP contract"
    }
    EOF

    FUND_PROP_TX=$($APP_NAME tx gov submit-proposal proposal_fund_lp.json \
      --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --chain-id "$CHAIN_ID" --node "$NODE_RPC" \
      --gas auto --gas-adjustment 1.3 --broadcast-mode sync --output json --yes)
    FUND_PROP_HASH=$(echo "$FUND_PROP_TX" | jq -r '.txhash')
    FUND_PROP_ID=$($APP_NAME query tx "$FUND_PROP_HASH" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json 2>/dev/null \
      | jq -r '.events[] | select(.type=="submit_proposal") | .attributes[] | select(.key=="proposal_id") | .value' | head -n1)
    echo "Voting YES on proposal $FUND_PROP_ID ..."
    $APP_NAME tx gov vote "$FUND_PROP_ID" yes --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --gas auto --yes
    ```

- Configure and start the bridge service
  - Set `BRIDGE_GETADDRESSES=http://api:9000/v1/bridge/addresses?chain=ethereum`, `BRIDGE_POSTBLOCK=http://api:9200/admin/v1/bridge/block` and start the `bridge/` container (Prysm + Geth in ingestion-only mode).
  - Validate with API: `GET /v1/bridge/status` and `GET /v1/bridge/addresses?chain=ethereum`.


- (For future changes in wrapped token base contract) Migrate existing wrapped tokens
  - Store the upgraded WASM file, then use `MsgMigrateAllWrappedTokens(newCodeId)` to migrate all existing instances.

  Instruction:
    ```bash
    APP_NAME=${APP_NAME:-inferenced}
    CHAIN_ID=${CHAIN_ID:-gonka-mainnet}
    KEY_NAME=${KEY_NAME:-node1}
    KEYRING_BACKEND=${KEYRING_BACKEND:-file}
    COIN_DENOM=${COIN_DENOM:-ngonka}
    HOME_DIR=${HOME_DIR:-~/mainnet}
    NODE_RPC=${NODE_RPC:-http://node2.gonka.ai:8000/chain-rpc/}

    # Step 1: Store upgraded wrapped-token WASM
    WT_WASM="inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm"
    echo "Storing upgraded wrapped-token wasm..."
    STORE_TX=$($APP_NAME tx wasm store "$WT_WASM" \
      --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" \
      --chain-id "$CHAIN_ID" --node "$NODE_RPC" --gas auto --gas-adjustment 1.3 \
      --broadcast-mode sync --output json --yes)
    TX_HASH=$(echo "$STORE_TX" | jq -r '.txhash')
    for i in $(seq 1 30); do
      TX_QUERY=$($APP_NAME query tx "$TX_HASH" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json 2>/dev/null || echo "")
      NEW_CODE_ID=$(echo "$TX_QUERY" | jq -r '.events[] | select(.type=="store_code") | .attributes[] | select(.key=="code_id") | .value' | head -n1)
      [ -n "$NEW_CODE_ID" ] && break; sleep 2; done
    [ -z "$NEW_CODE_ID" ] && echo "Error: new code_id not found" && exit 1
    echo "Upgraded wrapped-token NEW_CODE_ID=$NEW_CODE_ID"

    # Step 2: Submit migration proposal
    GOV_MODULE_ADDR=$($APP_NAME query auth module-accounts --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.accounts[] | select(.value.name=="gov") | .value.address')
    MIN_DEPOSIT_AMOUNT=$($APP_NAME query gov params --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json | jq -r '.params.min_deposit[] | select(.denom=="'"$COIN_DENOM"'") | .amount')

    cat > proposal_migrate_wrapped_tokens.json <<EOF
    {
      "messages": [{
        "@type": "/inference.inference.MsgMigrateAllWrappedTokens",
        "authority": "$GOV_MODULE_ADDR",
        "newCodeId": $NEW_CODE_ID,
        "migrateMsg": {}
      }],
      "deposit": "${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}",
      "title": "Migrate Wrapped Tokens",
      "summary": "Migrate all wrapped token instances to new code ID",
      "metadata": "https://github.com/gonka-ai/gonka/blob/main/proposals/bridge_smart_contract/README.md"
    }
    EOF

    echo "Submitting migration proposal..."
    MIGRATE_PROP_TX=$($APP_NAME tx gov submit-proposal proposal_migrate_wrapped_tokens.json \
      --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --chain-id "$CHAIN_ID" --node "$NODE_RPC" \
      --gas auto --gas-adjustment 1.3 --broadcast-mode sync --output json --yes)
    MIGRATE_PROP_HASH=$(echo "$MIGRATE_PROP_TX" | jq -r '.txhash')
    MIGRATE_PROP_ID=$($APP_NAME query tx "$MIGRATE_PROP_HASH" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --output json 2>/dev/null \
      | jq -r '.events[] | select(.type=="submit_proposal") | .attributes[] | select(.key=="proposal_id") | .value' | head -n1)
    echo "Voting YES on proposal $MIGRATE_PROP_ID ..."
    $APP_NAME tx gov vote "$MIGRATE_PROP_ID" yes --home "$HOME_DIR" --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --chain-id "$CHAIN_ID" --node "$NODE_RPC" --gas auto --yes
    ```


### Highlights

- Multi-chain exchange bridge integration between external chains and Gonka (initial adapter target EVM-family chains).
- CW20-based wrapped tokens on Gonka with module-controlled mint/burn and trading validation.
- Liquidity Pool CosmWasm contract registration and validation flow for wrapped tokens.
- New on-chain Msg/Query APIs for bridge, wrapped tokens, liquidity, and metadata.
- New public/admin HTTP API endpoints in `decentralized-api` for bridge ingestion and status.
- Bridge service container for execution/consensus clients with health-guarded logging and API hooks.
- BLS updates for bridge signing data and epoch index usage.

### New features

- Multi-chain exchange bridge
  - Inbound bridging (external → Gonka wrapped token W(EXT)):
    - Off-chain component ingests finalized blocks/receipts from external chains and submits `MsgBridgeExchange` to Gonka.
    - Chain mints or accounts for wrapped tokens W(EXT) mapped from the external token contract to a CW20 on Gonka.
  - Outbound bridging (Gonka native → external wrapped GNK/WGNK):
    - Users request BLS-backed `MsgRequestBridgeMint` to mint WGNK on a target chain while escrowing native GNK on Gonka.
  - Outbound withdrawal (WGNK → external):
    - Contract-only `MsgRequestBridgeWithdrawal` requests BLS signing to release tokens to an external destination.
  - Public API endpoints to submit finalized blocks and monitor queue/status; governance APIs to register bridge addresses per chain.

- Wrapped tokens (CW20 WGNK and external W(EXT))
  - Registry mapping external token → CW20 wrapped contract on Gonka.
  - Validation for trading via: module-as-minter check, token metadata, and approved-for-trade allowlist.
  - Queries to list wrapped balances for a Cosmos address and validate a wrapped token for trade.

- Liquidity Pool contract
  - Governance can instantiate a LP via `MsgRegisterLiquidityPool`.
  - Query exposes pool contract address and code ID.

### Improvements/Changes

- Chain
  - New protos: `bridge.proto`, `liquidity_pool.proto`. 
  - Removed legacy `bridge_transaction.proto` and `contracts.proto`.
  - Keepers and module wiring for bridge flows, wrapped tokens, and liquidity pool registration/queries.
  - BLS integration updated to use epoch index consistently across signing.
  - Genesis helpers and overrides updated.
  - AnteHandler update (LiquidityPoolFeeBypassDecorator): Adds a targeted fee-bypass path for specific liquidity-pool swap transactions to improve UX and prevent swap starvation while keeping gas metering intact. It only applies when:
    - Every message in the tx is a CosmWasm `MsgExecuteContract` (no mixed message types), and
    - The call is either a direct execute to the registered Liquidity Pool contract, or a CW20 `Send{contract:<pool>}` where the sender is a registered wrapped-token instance (matched by code ID), and
    - A configurable gas cap is respected; min-gas-prices are waived but gas usage is still metered, with an optional priority boost to avoid starvation.
    Why: enables smooth token swaps against the pool without requiring end-users to set explicit fees for these narrow, governance-controlled paths. Safety relies on governance-registered LP address and wrapped-token code ID, plus strict message shape checks.

- Decentralized API
  - Bridge queue (threshold + periodic processing) and endpoints to ingest blocks and expose status/addresses.
  - Cosmos client extended with `BridgeExchange` and `GetBridgeAddresses` helpers.

- Bridge service
  - New `bridge/` container launching execution (Geth) and beacon (Prysm) clients with robust restart and log formatting.
  - Custom flags to call the API for posting finalized blocks and fetching registered bridge addresses.

- Local test/deploy
  - Added `local-test-net/docker-compose.bridge.yml` and `docker-compose.contracts.yml`; launch scripts include new services.

### Bug fixes

- BLS correctness and genesis/test fixes; Pulsar-generated code and minor import issues.

### API changes

- Public REST (decentralized-api)
  - `GET /v1/bridge/status` — bridge queue depth, earliest/latest, readiness.
  - `GET /v1/bridge/addresses?chain=<name>` — registered bridge addresses for a chain.

- Admin REST (decentralized-api)
  - `POST /admin/v1/bridge/block` — submit finalized block with receipts for processing.

- CLI (autocli)
  - `request-bridge-mint [amount] [destination-address] [target-chain-id]` — user-initiated native GNK → WGNK mint on target chain (BLS-backed).
  - Governance (authority-gated; available as Msgs): register bridge addresses, register token metadata, approve tokens for trading, register wrapped-token code ID, migrate all wrapped tokens, register liquidity pool.

- gRPC-Gateway (inference module)
  - Bridge:
    - `GET /productscience/inference/inference/bridge_addresses/{chain_id}`
    - `GET /productscience/inference/inference/bridge_transaction/{origin_chain}/{block_number}/{receipt_index}`
    - `GET /productscience/inference/inference/bridge_transactions`
  - Liquidity pool:
    - `GET /productscience/inference/inference/liquidity_pool`
  - Wrapped tokens:
    - `GET /productscience/inference/inference/wrapped_token_balances/{address}`
    - `GET /productscience/inference/inference/validate_wrapped_token_for_trade/{contract_address}`
    - `GET /productscience/inference/inference/approved_tokens_for_trade`

### Chain & protocol changes

-- Messages (added)
  - `MsgRegisterBridgeAddresses`: Governance-only. Registers one or more bridge contract addresses for a given external chain (identified by `chainName`, e.g., `ethereum`, `polygon`). Used by off-chain adapters to restrict accepted events to trusted bridge contracts.
  - `MsgRegisterLiquidityPool`: Governance-only. Atomically instantiates and registers the singleton Liquidity Pool CosmWasm contract (code id, label, instantiate JSON). Enables token swaps directly against an on-chain liquidity pool (no order book) and enforces wrapped token trading checks.
  - `MsgRegisterTokenMetadata`: Governance-only. Stores metadata (name, symbol, decimals) for an external token identified by `(chainId, contractAddress)` so W(EXT) on Gonka can present accurate formatting and supply handling.
  - `MsgApproveBridgeTokenForTrading`: Governance-only. Adds an external token `(chainId, contractAddress)` to the allowlist for trading via Liquidity Pool. Complements metadata registration and prevents unvetted assets from trading.
  - `MsgRequestBridgeWithdrawal`: Contract-only. Called by a wrapped-token CW20 contract to initiate an external withdrawal (WGNK or W(EXT) → external chain). Triggers BLS signing over a message that encodes chain, operation, recipient, token contract, and amount.
  - `MsgRequestBridgeMint`: User-initiated. Locks native GNK on Gonka and requests BLS signing to mint a wrapped GNK (WGNK) on a target external chain. Includes destination address and target chain ID; uses epoch-indexed signing data.
  - `MsgRegisterWrappedTokenContract`: Governance-only. Records the code ID to use when instantiating future wrapped-token contracts, enabling coordinated upgrades and consistent mint authority (the inference module as minter).
  - `MsgMigrateAllWrappedTokens`: Governance-only. Batch migrates all known wrapped-token instances to a new code ID with an optional migrate message, to roll out contract-level fixes or features.

-- Messages (existing used by bridge ingestion)
  - `MsgBridgeExchange`: Signed by Host/Epoch's Active Validator. Submitted by the decentralized API after ingesting finalized external receipts. Records the external transfer on-chain, enabling minting/accounting of W(EXT) balances and anti-replay by composite ID `(originChain, blockNumber, receiptIndex)`.

-- Queries (added)
  - `BridgeAddressesByChain`: Returns currently registered bridge contract addresses for a specific chain. Used by off-chain adapters and UIs to verify source authenticity.
  - `LiquidityPool`: Returns the singleton Liquidity Pool contract address, code ID, and the height of registration. Powers UI discovery and integrations.
  - `WrappedTokenBalances`: Lists balances of all registered wrapped tokens (CW20) for a given Cosmos address, including symbol, decimals, and a human-formatted balance.
  - `ValidateWrappedTokenForTrade`: Validates that a CW20 contract address is an authorized wrapped token for trading. Checks: minter is the module, metadata exists, token is allowlisted.
  - `ApprovedTokensForTrade`: Returns the allowlisted external tokens `(chainId, contractAddress)` approved for trading via LP.

-- Type details and fields
  - `BridgeContractAddress`
    - `id`: internal unique key for the stored address entry
    - `chainId`: external chain identifier (e.g., `ethereum`, `sepolia`, `polygon`)
    - `address`: bridge contract address on the external chain
    - Purpose: restrict which external contracts we accept events from

  - `BridgeTokenMetadata`
    - `chainId`: external chain identifier for the token
    - `contractAddress`: external token (ERC-20 or other) contract address
    - `name`: token name (e.g., "USDC")
    - `symbol`: token ticker (e.g., "USDC")
    - `decimals`: number of decimal places used by the token
    - Purpose: render balances correctly on Gonka and standardize UX for W(EXT)

  - `BridgeTokenReference`
    - `chainId`: external chain identifier
    - `contractAddress`: external token contract address
    - Purpose: compact identifier used for allowlists and lookups (e.g., approved for trading)

  - `BridgeTransaction`
    - `id`: unique transaction key (composed from origin chain + block number + receipt index)
    - `chainId`: origin external chain identifier
    - `contractAddress`: origin external token/bridge contract involved
    - `ownerAddress`: Cosmos (Gonka) address that receives/owns the wrapped asset on-chain
    - `amount`: amount bridged (as string to support big integers)
    - `status`: `BRIDGE_PENDING` or `BRIDGE_COMPLETED`
    - `blockNumber`: origin chain block number containing the transfer receipt
    - `receiptIndex`: index of the receipt within that block
    - `receiptsRoot`: Merkle root of the origin block receipts trie
    - `epochIndex`: BLS epoch when the transaction was validated/signed
    - `validators`: list of validator addresses that participated in validation
    - `totalValidationPower`: sum of voting power that signed/validated
    - Purpose: canonical on-chain record of inbound external transfers for minting/accounting of W(EXT)

  - `BridgeWrappedTokenContract`
    - `chainId`: origin external chain identifier for the underlying token
    - `contractAddress`: origin token contract on the external chain
    - `wrappedContractAddress`: CW20 contract address on Gonka that represents W(EXT)
    - Purpose: maps external tokens to their wrapped CW20 instances on Gonka

  - `LiquidityPool`
    - `address`: CosmWasm contract address of the liquidity pool
    - `codeId`: uploaded code id used to instantiate the contract
    - `block_height`: block height at which the pool was registered
    - Purpose: discoverability and auditability of the on-chain pool used for token swaps

  - `WrappedTokenBalance`
    - `tokenInfo`: the `BridgeWrappedTokenContract` for this balance entry
    - `symbol`: token ticker obtained from metadata (if available)
    - `balance`: raw CW20 balance as a string (big integer)
    - `decimals`: the decimals used by the underlying token (as string for display)
    - `formatted_balance`: human-ready string derived from `balance` and `decimals`
    - Purpose: API-friendly response for wallets and UIs to show W(EXT) holdings

### Genesis changes

- Removed cw20-base from genesis
  - We no longer include cw20-base code or instances in genesis. Wrapped tokens are instantiated post-genesis via governance-controlled flows, using the registered wrapped-token code ID. This reduces attack surface in genesis, avoids pre-seeding contracts that might need upgrades, and aligns with the migration path using `MsgRegisterWrappedTokenContract` + `MsgMigrateAllWrappedTokens`.

- Bridge-related state
  - `Bridge` fields (if provided):
    - `contract_addresses`: trusted external bridge contracts per `chainId`.
    - `token_metadata`: pre-registered `(chainId, contractAddress)` metadata for proper decimals/symbols.
    - `trade_approved_tokens`: allowlist of external tokens approved for trading.

- Liquidity Pool and wrapped token code id
  - `liquidity_pool` can be omitted at genesis and registered later via `MsgRegisterLiquidityPool`.
  - `wrapped_token_code_id` can be omitted at genesis and set later via `MsgRegisterWrappedTokenContract`.

### Smart contracts

- Wrapped token (CW20-based WGNK / W(EXT))
  - CW20 standard: `transfer`, `transfer_from`, `approve`/`allowance`, `balance`, `token_info`.
  - Minting: only the chain’s inference module can mint. Used when external transfers are bridged into Gonka (W(EXT)).
  - Withdrawal initiation: exposes an execute path for contract-initiated withdrawals; the contract triggers a chain message to request an external withdrawal, and tokens are burned/escrowed for settlement by the bridge.
  - Metadata sync: metadata (name/symbol/decimals) is maintained via on-chain `MsgRegisterTokenMetadata`; contract state is updated accordingly by the chain.
  - Governance/admin: governance controls code upgrades (migrate) and the registered code ID for future instantiations.
  - Upgradeable: governance can register a new code ID and migrate all instances via `MsgRegisterWrappedTokenContract` + `MsgMigrateAllWrappedTokens` (supports no-op `{}` migrate messages).
  - File: `inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm`
  - Checksum: `f0833d6eafcb573f4fdaf6cb6dcdc03350ac27cf5749eb086359552bc0141f98`
  - Test Scripts: `deploy_wrapped_token.sh`, `update_wrapped_token.sh`


- Liquidity pool
  - Add liquidity: deposit supported wrapped tokens to increase pool reserves and receive pool (LP) shares representing pro‑rata ownership.
  - Remove liquidity: redeem LP shares to withdraw a proportional share of the underlying reserves.
  - Swap: swap supported wrapped tokens directly against the pool reserves (no order book). Pricing is derived from current reserves; slippage grows with trade size relative to pool depth.
  - Token eligibility: accepts swaps only for wrapped tokens that pass chain validation (module-as-minter, metadata present) and are approved for trading.
  - Governance/admin: governance instantiates the pool and can upgrade the contract via standard migration procedures.
  - File: `inference-chain/contracts/liquidity-pool/artifacts/liquidity_pool.wasm`
  - Checksum: `1486a9817798631da5b2a4e0e443c1c6b1c202074e9ef4dca25663f0c2193fca`
  - Test Scripts: `deploy_liquidity_pool.sh`, `fund_liquidity_pool.sh`
  - Upgradeable: instantiated with governance as admin; can be upgraded to newer WASM via standard CosmWasm migrate proposal if needed.