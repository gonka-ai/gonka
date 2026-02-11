# Community Sale Contract

CosmWasm contract for GNK sale to a single buyer via Ethereum bridge.

## Purpose

Fixed-price exchange of W(USDT) for GNK. Accepts payments only from a designated buyer address.

## Parameters (set at deployment)

- `admin` - receives W(USDT), can withdraw unsold GNK (typically governance module)
- `buyer` - only address allowed to purchase
- `accepted_chain_id` - external chain ID (e.g., "ethereum")
- `accepted_eth_contract` - token contract on external chain (e.g., "0xdac17f958d2ee523a2206206994597c13d831ec7" for USDT)
- `price_usd` - fixed price per 1 GNK in micro-USD (6 decimals, e.g., 25000 = $0.025/GNK)

## Deployment

Anyone can deploy. Set governance module as admin to receive funds.

### 1. Build the contract

```bash
cd inference-chain/contracts/community-sale
./build.sh
```

### 2. Store the WASM code

```bash
./inferenced tx wasm store artifacts/community_sale.wasm \
    --from your-key \
    --gas auto --gas-adjustment 1.3 \
    --broadcast-mode sync --output json --yes
```

Save the `code_id` from the response.

### 3. Instantiate the contract

```bash
./inferenced tx wasm instantiate <CODE_ID> \
    '{"admin":"gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33","buyer":"gonka1...buyer","accepted_chain_id":"ethereum","accepted_eth_contract":"0xdac17f958d2ee523a2206206994597c13d831ec7","price_usd":"25000"}' \
    --label "community-sale-v1" \
    --admin gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33 \
    --from your-key \
    --gas auto --gas-adjustment 1.3 \
    --broadcast-mode sync --output json --yes
```

Parameters:
- `admin` - governance module address (`gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33`) receives W(USDT)
- `buyer` - designated buyer address (only this address can purchase)
- `accepted_chain_id` - external chain ID ("ethereum")
- `accepted_eth_contract` - USDT contract on Ethereum (`0xdac17f958d2ee523a2206206994597c13d831ec7`)
- `price_usd` - price per 1 GNK in micro-USD (25000 = $0.025/GNK)
- `--admin` flag - WASM migration admin (set to governance for upgrades via proposals)

### 4. Fund the contract with GNK

Governance proposal to transfer GNK from community pool:

```json
{
    "messages": [{
        "@type": "/cosmos.distribution.v1beta1.MsgCommunityPoolSpend",
        "authority": "gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33",
        "recipient": "<CONTRACT_ADDRESS>",
        "amount": [{"denom": "ngonka", "amount": "1000000000000000"}]
    }],
    "deposit": "50000000ngonka",
    "title": "Fund Community Sale Contract",
    "summary": "Transfer 1M GNK to community sale contract"
}
```

## Workflow

1. Deploy contract with buyer address, Ethereum USDT address, price, governance as admin
2. Governance proposal transfers GNK from community pool to contract
3. Buyer sends W(USDT) via CW20 Send, receives GNK proportionally
4. Contract validates token via chain (ApprovedTokensForTrade) and queries BridgeInfo
5. W(USDT) forwarded to admin (governance module)
6. If buyer doesn't complete purchase, governance withdraws remaining GNK via proposal

## Purchase Flow

Buyer calls Send on the W(USDT) CW20 contract:

```json
{
    "send": {
        "contract": "<SALE_CONTRACT>",
        "amount": "1000000000",
        "msg": "e30="
    }
}
```

The `msg` is base64-encoded `{}` (empty JSON object).

## Admin Operations (governance proposals)

- `Pause {}` - pause the contract
- `Resume {}` - resume the contract
- `UpdateBuyer { buyer }` - change designated buyer
- `UpdatePrice { price_usd }` - change price
- `WithdrawNativeTokens { amount, recipient }` - withdraw unsold GNK
- `EmergencyWithdraw { recipient }` - withdraw all GNK

## Security

- Only validated bridge tokens accepted (chain's ApprovedTokensForTrade)
- Contract queries CW20's BridgeInfo to verify underlying Ethereum contract
- Only designated buyer can purchase
