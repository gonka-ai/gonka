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
        B1["MsgRegisterBridgeAddresses<br/>(chainId + bridge contract addr)"]
        B2["MsgSetWrappedTokenCodeID<br/>(CW20 code to instantiate)"]
        B3["MsgSetTokenMetadata<br/>(name / symbol / decimals)"]
        B4["MsgApproveBridgeTokenForTrade<br/>(chainId + original contract addr)"]
    end

    subgraph IBC_SETUP["IBC Token Path"]
        I1["MsgApproveIbcTokenForTrading<br/>(chainId='ibc', ibc/HASH)"]
        I2["MsgRegisterIbcTokenMetadata<br/>(name / symbol / decimals)<br/>[optional if x/bank already has it]"]
    end

    G1 --> B1
    G1 --> B2
    G1 --> B3
    G1 --> B4
    G1 --> I1
    G1 --> I2

    B1 -->|"BridgeContractAddresses (chainId, address)"| S1[(Chain State)]
    B4 -->|"LiquidityPoolApprovedTokensMap (chainId, contractAddress)"| S1
    I1 -->|"LiquidityPoolApprovedTokensMap (chainId, ibc/HASH)"| S1
    I2 -->|"WrappedTokenMetadataMap + x/bank denom metadata"| S1
```

---

## 1a · Bridge Address Registration — Conflict Guard (GEB-46)

> What happens inside `MsgRegisterBridgeAddresses` when the address being registered already
> exists as a CW20 wrapped token (either from a legitimate earlier registration or from a
> fraudulent `MsgBridgeExchange` during the governance voting window).

```mermaid
flowchart TD
    GOV["🏛️ MsgRegisterBridgeAddresses\npassed by governance"]
    GOV --> LOOP["For each address in proposal"]

    LOOP --> ALREADY{"Already in\nBridgeContractAddresses?"}
    ALREADY -->|yes| SKIP["⚠️ LogWarn: already registered\nSkip — no change"]
    ALREADY -->|no| CHECK_CW20

    CHECK_CW20{"WrappedTokenContractsMap\nhas entry for this address?"}
    CHECK_CW20 -->|no| REGISTER
    CHECK_CW20 -->|"yes (stale / fraudulent CW20)"| CLEANUP

    CLEANUP["⚠️ LogWarn: removing stale wrapped token record\n─────────────────────────────\n1. WrappedTokenContractsMap.Remove(chainId, address)\n2. WrappedContractReverseIndex.Remove(orphanedCW20addr)\n─────────────────────────────\nOrphaned CW20 contract still exists on-chain\nbut is now UNREACHABLE from x/inference.\nRequestBridgeWithdrawal will reject it."]
    CLEANUP --> REGISTER

    REGISTER["✅ BridgeContractAddresses.Set(chainId, address)"]
    REGISTER --> LOOP
```

### Why This Matters

If attacker validators co-sign a fake `MsgBridgeExchange` during the 24-hour governance voting
window — specifying the would-be bridge address as a token contract — the system would
incorrectly create a CW20 entry for that address. Once governance passes and the address enters
`BridgeContractAddresses`, the stale CW20 would create a routing collision:
- **Inbound** transactions correctly hit the native escrow-release path (the bridge-address check wins)
- But the orphaned CW20 could still be burned via `RequestBridgeWithdrawal`, generating a valid
  BLS-signed withdrawal payload where `tokenContract == address(BridgeContract)` on Ethereum —
  triggering the ETH-release branch in `withdraw()` and draining real funds.

The cleanup in `MsgRegisterBridgeAddresses` atomically closes this window the moment governance passes.
A second guard in `GetOrCreateWrappedTokenContract` prevents the collision state from ever being
created once the bridge address is registered.

| Guard | Location | Prevents |
|---|---|---|
| Cleanup on registration | `msg_server_register_bridge_addresses.go` | Stale CW20 surviving after governance passes |
| Pre-create check | `bridge_wrapped_token.go → GetOrCreateWrappedTokenContract` | New CW20 creation for any registered bridge address |

---

## 2 · Bridge (Ethereum → Gonka) Token Inbound


```mermaid
sequenceDiagram
    participant User as 👤 User (Ethereum)
    participant Eth as Ethereum Contract
    participant Relayer as 🔄 Geth Bridge Relayer
    participant Val as 🛡️ Gonka Validators (BLS)
    participant Chain as ⚙️ x/inference Module
    participant CW20 as 🪙 Wrapped CW20 Contract

    User->>Eth: Transfer ERC-20 token (Deposit to Bridge / Burn)
    Eth-->>Relayer: Emits Transfer event (to Bridge or to Burn address)
    Note over Relayer: Receipt Sync Mode — bypasses EVM execution
    Relayer->>Relayer: Filter: Match Transfer to bridge address (Deposit)
    Relayer->>Relayer: Filter: Match Transfer to zero/dead address (Burn)
    Relayer->>Relayer: Recover user full public key from Tx signature
    Relayer->>Val: POST filtered receipts + PubKey to Bridge API
    Val->>Val: Accumulate DKG/BLS threshold signatures
    Val->>Chain: MsgBridgeExchange (majority vote / aggregated sig)
    Chain->>Chain: Verify (originChain, contractAddress) registered [GEB-15]
    alt Native token (WGNK) released
        Chain->>User: Release from bridge escrow (BankMsg)
    else Foreign ERC-20
        Chain->>CW20: GetOrCreateWrappedTokenContract
        Chain->>CW20: Mint wrapped tokens to recipient
    end
```

---

## 2a · Geth Bridge Relayer — Transaction Filtering Detail

> How `eth/bridge/bridge.go` → `ProcessBlocks()` decides which ERC-20 transfers get forwarded to the Cosmos bridge node.

```mermaid
flowchart TD
    START(["New block: headers + bodies + receipts<br/>arrived via ReceiptSync"]) --> CFG

    CFG{"BridgePostBlockEP &<br/>BridgeGetAddressesEP set?"}
    CFG -->|no| SKIP(["Skip — bridge not configured"])
    CFG -->|yes| FETCH

    FETCH["GET BridgeGetAddressesEP<br/>→ fetch registered bridge contract addresses"]
    FETCH -->|error| ERR1(["Log error & return"])
    FETCH -->|empty list| SKIP2(["Log & skip block"])
    FETCH -->|ok| BUILDSET

    BUILDSET["Build bridgeAddrSet<br/>map[address]struct{}"]
    BUILDSET --> ITER

    ITER["Iterate receipts<br/>(receipt index i = tx index i)"]
    ITER --> RECEIPT

    RECEIPT{"receipt == nil<br/>or no Logs?"}
    RECEIPT -->|yes| NEXT
    RECEIPT -->|no| LOG_ITER

    LOG_ITER["Iterate receipt.Logs"] --> TOPICHECK

    TOPICHECK{"Topics[0] == keccak256('Transfer(address,address,uint256)')<br/>AND len(Topics) >= 3?"}
    TOPICHECK -->|no| NEXT
    TOPICHECK -->|yes| CLASSIFY

    CLASSIFY["from = Topics[1]<br/>to = Topics[2]<br/>contract = log.Address"]

    CLASSIFY --> D1

    D1{"to ∈ bridgeAddrSet?"}
    D1 -->|yes| DEPOSIT["✅ DEPOSIT<br/>(user locked tokens in bridge)"]
    D1 -->|no| D2

    D2{"contract ∈ bridgeAddrSet<br/>AND to == 0x0 or 0x...dEaD<br/>AND from != 0x0?"}
    D2 -->|yes| BURN["✅ BURN / WITHDRAW<br/>(bridge burned token)"]
    D2 -->|no| NEXT["Next log / receipt"]

    DEPOSIT --> TXLOOKUP
    BURN --> TXLOOKUP

    TXLOOKUP{"i < len(transactions)?"}
    TXLOOKUP -->|no| WARN(["Log warn: tx index OOB — skip receipt"])
    TXLOOKUP -->|yes| SENDER

    SENDER["types.Sender(signer, tx)<br/>→ recover from address"]
    SENDER -->|error| WARN2(["Log warn: skip receipt"])
    SENDER -->|ok| PUBKEY

    PUBKEY["crypto.SigToPub(sighash, sig)<br/>→ recover 33-byte compressed EC public key"]
    PUBKEY -->|error| WARN3(["Log warn: skip receipt"])
    PUBKEY -->|ok| VERIFYMATCH

    VERIFYMATCH{"PubKeyToAddress ==<br/>recovered sender?"}
    VERIFYMATCH -->|no| WARN4(["Log warn: address mismatch — skip receipt"])
    VERIFYMATCH -->|yes| BUILD

    BUILD["Build ReceiptData<br/>• contract = log.Address<br/>• owner = from (Eth addr)<br/>• publicKey = compressed pubkey hex<br/>• amount = log.Data as big.Int<br/>• receiptIndex = i"]

    BUILD --> APPEND["Append to filteredReceipts<br/>Break inner log loop (one event per tx)"]
    APPEND --> NEXT

    NEXT --> DONE{"All receipts processed?"}
    DONE -->|no| ITER
    DONE -->|yes| POST

    POST["POST BridgePostBlockEP — BlockRequest:<br/>• blockNumber<br/>• originChain = 'ethereum'<br/>• receiptsRoot (hex)<br/>• receipts = filteredReceipts<br/>(sent even if list is empty)"]

    POST -->|error| ERR2(["Log error & return err"])
    POST -->|"200 OK"| FIN(["Block reported to Cosmos Bridge Node ✅"])
```

### Filtering Rules Summary

| Rule | Condition | Classification | Description |
|---|---|---|---|
| **Deposit** | ERC-20 `Transfer` where `to` ∈ bridge contract addresses | ✅ Deposit (lock) | The user sent tokens **into** a registered bridge contract. The bridge contract address set is fetched fresh from `BridgeGetAddressesEP` on every block, so newly registered contracts are picked up automatically without a relayer restart. |
| **Burn** | ERC-20 `Transfer` where the **emitting contract** ∈ bridge addresses AND `to` == `0x0000…0000` or `0x000…dEaD` AND `from` ≠ `0x0` | ✅ Burn (withdraw) | The bridge token contract itself burned tokens to a well-known dead address, signalling a withdrawal back to Ethereum. The `from ≠ 0x0` guard explicitly excludes **mint** events (which appear as Transfer from `0x0`), so mints are never misclassified as burns. The two accepted burn addresses are the EVM zero address and the canonical `0xdead` address. |
| **Skip** | `Topics[0]` is not `keccak256("Transfer(address,address,uint256)")` OR fewer than 3 topics | ⛔ Ignored | Non-Transfer events (e.g. `Approval`, custom events) are discarded at the topic-signature gate before any address matching is attempted. Logs with fewer than 3 topics are malformed Transfer events and are also dropped. |
| **Skip** | Transfer does not match deposit OR burn criteria | ⛔ Ignored | Ordinary peer-to-peer ERC-20 transfers between two user wallets produce no receipt entry. Only transfers **to** bridge contracts or **from** bridge contracts **to** burn addresses are relevant to the bridge. |
| **Skip** | Receipt index `i` ≥ number of transactions in block | ⛔ Skipped with warning | Guards against a data-consistency bug where receipts and transactions are misaligned. Logged as a warning so it is visible in metrics without halting the relayer. |
| **Skip** | `types.Sender()` recovery fails | ⛔ Skipped with warning | The transaction signature is invalid or the signer type cannot be determined. The receipt is dropped; the block POST still proceeds with other valid receipts. |
| **Skip** | `crypto.SigToPub()` fails or recovered address ≠ `types.Sender()` | ⛔ Skipped with warning | The full uncompressed public key could not be recovered, or the key does not hash back to the expected Ethereum address. The public key is required by the Cosmos bridge to verify the depositor's identity on-chain, so the receipt is dropped if it cannot be reliably obtained. |
| **One event per tx** | After the first matching log in a receipt, the inner log loop `break`s | ℹ️ At most one ReceiptData per transaction | Prevents double-counting when a single transaction emits multiple Transfer events (e.g. a router hop). Only the first matching bridge-relevant log in each receipt is forwarded. |

---

## 3 · Bridge (Gonka → Ethereum) Token Outbound

```mermaid
sequenceDiagram
    participant User as 👤 User (Gonka)
    participant Chain as ⚙️ x/inference Module
    participant Val as 🛡️ Gonka Validators (DKG/BLS)
    participant Relayer as 🔄 Geth Bridge Relayer
    participant Eth as Ethereum Contract

    User->>Chain: Trigger MsgWithdrawBridgeToken
    Chain->>Chain: Lock WGNK or Burn CW20
    Chain->>Val: Request threshold signature for Payload
    Val->>Val: Accumulate DKG threshold signature
    Val-->>Chain: Signature generation success event
    Chain->>Chain: Record aggregated signature on-chain
    Chain-->>Relayer: Emit on-chain EVM-compatible withdrawal event & signature
    Relayer->>Relayer: Poll & read event from Gonka RPC
    Relayer->>Eth: Submit withdrawal transaction with signature
    Eth->>Eth: Cryptographically verify aggregated signature
    Eth->>User: Release ERC-20 / ETH to recipient
```

---

## 4 · Trading in the Liquidity Pool

```mermaid
flowchart LR
    U1["👤 Send CW20<br/>(Receive hook)"]
    U2["👤 Send IBC coin<br/>(PurchaseWithNative)"]

    subgraph CW["CW20 Bridge Path"]
        direction TB
        V1A["1. Approved?<br/>LiquidityPoolApprovedTokensMap"]
        V1B["2. Get decimals<br/>CW20 token_info query"]
        ERR1["❌ Rejected"]
        V1A -->|yes| V1B
        V1A -->|no| ERR1
    end

    subgraph IBC["IBC Token Path"]
        direction TB
        V2A["1. Approved?<br/>LiquidityPoolApprovedTokensMap"]
        V2B["2a. Custom metadata?<br/>WrappedTokenMetadataMap"]
        V2C["2b. Fallback: x/bank<br/>strict validation"]
        ERR2["❌ Rejected"]
        V2A -->|yes| V2B
        V2A -->|no| ERR2
        V2B -->|found| VDONE["✔ decimals resolved"]
        V2B -->|not found| V2C
        V2C -->|valid| VDONE
        V2C -->|invalid| ERR2
    end

    CALC["3. normalize_to_usd<br/>multi_tier_purchase<br/>daily limit check"]
    SEND["💸 Send ngonka to buyer"]
    ACC["💰 Payment accumulates<br/>in contract"]

    U1 --> CW
    U2 --> IBC
    V1B --> CALC
    VDONE --> CALC
    CALC --> SEND
    CALC --> ACC
```

---

## 5 · Admin Withdrawal

```mermaid
flowchart LR
    ACC["Contract Balance<br/>(accumulated payments)"]
    ACC -->|"WithdrawCw20"| ADM["Admin / Treasury"]
    ACC -->|"WithdrawIbc"| ADM
    ACC -->|"WithdrawNative"| ADM
    ACC -->|"EmergencyWithdraw"| ADM
```

---

## 6 · Unified Token Discovery (UI / Frontend)

```mermaid
flowchart LR
    Q["ApprovedTokensForTrade query"] --> MAP["LiquidityPoolApprovedTokensMap<br/>(single shared store)"]
    MAP --> CW["CW20 bridge tokens<br/>chainId=ethereum, addr=0x..."]
    MAP --> IBC["IBC tokens<br/>chainId=ibc, addr=ibc/HASH<br/>canonical uppercase returned"]
    CW -->|"ValidateWrappedTokenForTrade"| UI["Frontend"]
    IBC -->|"ValidateIbcTokenForTrade (decimals included)"| UI
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
