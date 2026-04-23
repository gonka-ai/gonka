# IBC & Wrapped Token Cycle

Two parallel token paths lead into the Gonka Liquidity Pool. Both go through governance approval before any user can trade with them.

---

## 1 · Governance Setup Phase

```mermaid
flowchart TD
    subgraph GOV["🏛️ Governance"]
        G1["Submit Proposal"]
    end

    subgraph BRIDGE_SETUP["Bridge (CW20) Path"]
        B1["MsgRegisterBridgeAddresses\n(chainId + bridge contract addr)"]
        B2["MsgSetWrappedTokenCodeID\n(CW20 code to instantiate)"]
        B3["MsgSetTokenMetadata\n(name / symbol / decimals)"]
        B4["MsgApproveBridgeTokenForTrade\n(chainId + original contract addr)"]
    end

    subgraph IBC_SETUP["IBC Token Path"]
        I1["MsgApproveIbcTokenForTrading\n(chainId='ibc', ibc/HASH)"]
        I2["MsgRegisterIbcTokenMetadata\n(name / symbol / decimals)\n[optional if x/bank already has it]"]
    end

    G1 --> B1
    G1 --> B2
    G1 --> B3
    G1 --> B4
    G1 --> I1
    G1 --> I2

    B1 -->|"BridgeContractAddresses\n(chainId, address)"| S1[(Chain State)]
    B4 -->|"LiquidityPoolApprovedTokensMap\n(chainId, contractAddress)"| S1
    I1 -->|"LiquidityPoolApprovedTokensMap\n(chainId, ibc/HASH)\noriginal casing preserved"| S1
    I2 -->|"WrappedTokenMetadataMap\n+ x/bank denom metadata"| S1
```

---

## 2 · Bridge (Ethereum → Gonka) Token Inbound

```mermaid
sequenceDiagram
    participant User as 👤 User (Ethereum)
    participant Eth as Ethereum Contract
    participant Val as Gonka Validators
    participant Chain as x/inference Module
    participant CW20 as Wrapped CW20 Contract

    User->>Eth: Lock / burn ERC-20 token
    Eth-->>Val: Emit on-chain event
    Val->>Chain: MsgBridgeExchange (majority vote 50%+1)
    Chain->>Chain: Verify (originChain, contractAddress) registered\n[GEB-15: chain-scoped check]
    alt Native token (WGNK) released
        Chain->>User: Release from bridge escrow (BankMsg)
    else Foreign ERC-20
        Chain->>CW20: GetOrCreateWrappedTokenContract
        Chain->>CW20: Mint wrapped tokens to recipient
    end
```

---

## 3 · Trading in the Liquidity Pool

```mermaid
flowchart LR
    U1["👤 Send CW20\n(Receive hook)"]
    U2["👤 Send IBC coin\n(PurchaseWithNative)"]

    subgraph CW["CW20 Bridge Path"]
        direction TB
        V1A["1. Approved?\nLiquidityPoolApprovedTokensMap"]
        V1B["2. Get decimals\nCW20 token_info query"]
        ERR1["❌ Rejected"]
        V1A -->|yes| V1B
        V1A -->|no| ERR1
    end

    subgraph IBC["IBC Token Path"]
        direction TB
        V2A["1. Approved?\nLiquidityPoolApprovedTokensMap"]
        V2B["2a. Custom metadata?\nWrappedTokenMetadataMap"]
        V2C["2b. Fallback: x/bank\nstrict validation"]
        ERR2["❌ Rejected"]
        V2A -->|yes| V2B
        V2A -->|no| ERR2
        V2B -->|found| VDONE["✔ decimals resolved"]
        V2B -->|not found| V2C
        V2C -->|valid| VDONE
        V2C -->|invalid| ERR2
    end

    CALC["3. normalize_to_usd\nmulti_tier_purchase\ndaily limit check"]
    SEND["💸 Send ngonka to buyer"]
    ACC["💰 Payment accumulates\nin contract"]

    U1 --> CW
    U2 --> IBC
    V1B --> CALC
    VDONE --> CALC
    CALC --> SEND
    CALC --> ACC
```

---

## 4 · Admin Withdrawal

```mermaid
flowchart LR
    ACC["Contract Balance\n(accumulated payments)"]
    ACC -->|"WithdrawCw20"| ADM["Admin / Treasury"]
    ACC -->|"WithdrawIbc"| ADM
    ACC -->|"WithdrawNative"| ADM
    ACC -->|"EmergencyWithdraw"| ADM
```

---

## 5 · Unified Token Discovery (UI / Frontend)

```mermaid
flowchart LR
    Q["ApprovedTokensForTrade query"] --> MAP["LiquidityPoolApprovedTokensMap\n(single shared store)"]
    MAP --> CW["CW20 bridge tokens\nchainId=ethereum, addr=0x..."]
    MAP --> IBC["IBC tokens\nchainId=ibc, addr=ibc/HASH\ncanonical uppercase returned"]
    CW -->|"ValidateWrappedTokenForTrade"| UI["Frontend"]
    IBC -->|"ValidateIbcTokenForTrade\ndecimals included"| UI
```

---

## Key Design Principles

| Concern | CW20 (Bridge) | IBC |
|---|---|---|
| **Registration** | `MsgRegisterBridgeAddresses` + `MsgApproveBridgeTokenForTrade` | `MsgApproveIbcTokenForTrading` |
| **Metadata** | `MsgSetTokenMetadata` (custom store) | `MsgRegisterIbcTokenMetadata` → also writes x/bank |
| **Decimals source** | CW20 `token_info` query | governance store → fallback to x/bank |
| **Validation query** | `ValidateWrappedTokenForTrade` | `ValidateIbcTokenForTrade` |
| **Payment accumulation** | Contract holds CW20 | Contract holds IBC coins |
| **Admin withdrawal** | `WithdrawCw20` | `WithdrawIbc` |
| **Approval store** | `LiquidityPoolApprovedTokensMap` | same map (unified) |
| **Casing** | lowercase normalized | original casing preserved (`ibc/UPPERCASE`) |
