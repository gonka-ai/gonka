# Proposal: Enable IBC Token Trading via Governance

## Overview
This proposal enables the trading of **Native and IBC Tokens** in the Gonka Liquidity Pool and Community Sale contract by introducing a dedicated governance approval workflow. This is specifically designed to support stablecoin representations in the Cosmos ecosystem (such as `USDC` or `USDT`) as alternative to CW20 bridge wrapper.

## Core Design Principles
1.  **Strict Separation of Logic**: IBC-specific logic lives in separate files (`ibc_wrapped_token.go`) and uses separate messages (`MsgApproveIbcTokenForTrading`).
2.  **Unified Available List**: While logic is separate, approvals are stored in the *same* underlying map (`LiquidityPoolApprovedTokensMap`). This ensures the existing `ApprovedTokensForTrade` query automatically returns both CW20 and IBC tokens to UIs.
3.  **Distinct Validation Paths**: The Liquidity Pool Contract will use a *new* specific query (`ValidateIbcTokenForTrade`) for IBC tokens, acknowledging that their metadata lives in the Bank module, not the Bridge store.

## Changes

### 1. Chain Logic (`inference-chain`)

#### A. New Protocol Definitions
*   **Message**: `MsgApproveIbcTokenForTrading`
    *   **Usage**: Governance submits this to valid an IBC denom.
    *   **Convention**: `ChainId="ibc"`, `ContractAddress="ibc/HASH"` (or native denom).
*   **Query**: `ValidateIbcTokenForTrade`
    *   **Usage**: Called by the Liquidity Pool contract to verify an IBC token before adding it.

#### B. Implementation (`x/inference/keeper`)
We introduce new files to mirror the existing bridge functionality but adapted for IBC:
*   `ibc_wrapped_token.go`: Handles storage of approvals (allowing `/` in regex).
*   `query_ibc_token.go`: Handles validation logic.
    *   **Metadata Check**: Verifies `BankKeeper.GetDenomMetaData(ctx, denom)` exists (ensuring UI has decimals/symbol).
    *   **Allowlist Check**: Verifies the token is in `LiquidityPoolApprovedTokensMap`.
*   `msg_server_register_ibc_token_metadata.go`: Handles dual-writing of metadata.
    *   **Internal Map**: Stores metadata in `WrappedTokenMetadataMap` for bridge-specific logic.
    *   **Bank Module integration**: Calls `BankKeeper.SetDenomMetaData` to ensure the IBC token decimals and symbol are visible via standard Cosmos bank queries and explorers.

#### C. Contract Migration Integration (`app/upgrades/v0_2_11`)
*   **Upgrade Handler**: Added CosmWasm contract migration steps directly into the consensus upgrade.
*   **Execution**: Parses the governance `Plan.Info` field for a JSON string containing `community_sale_address` and `new_code_id`. Utilizing `wasmkeeper`, it safely executes the contract migration, automatically triggering the update payload to enable new configuration `allow_all_trade_tokens`.

### 2. Contract Logic (`liquidity-pool`)

#### A. Dynamic Token Validation
*   **Method**: `validate_wrapped_token_for_trade` and `validate_ibc_token_for_trade`
*   **Logic Change**: The concept of a maintained `PAYMENT_TOKENS` map has been removed. The contract will now dynamically detect the token type and validate it against the chain module at the moment of purchase:
    *   **CW20**: Calls legacy `ValidateWrappedTokenForTrade`.
    *   **IBC/Native**: Calls new `ValidateIbcTokenForTrade`.
*   **Result**: Validates the token and retrieves necessary decimals directly from the chain API as the source of truth, removing duplicate state handling in the contract.

#### B. Native Token Purchase & Fund Accumulation
*   **Target**: `ExecuteMsg::PurchaseWithNative` and CW20 `Receive`
*   **Logic**:
    *   Accepts `info.funds` (strictly 1 coin).
    *   Dynamically validates the token and normalizes its USD value using the decimals sourced from the chain.
    *   **Accumulation**: Instead of forwarding received tokens to the `admin` account immediately, both CW20 and Native/IBC tokens safely accumulate in the contract's balance. This design change was necessary because the governance (admin) account does not have the ability to sign and execute CW20 smart contract transfer messages. If forwarded directly to governance, CW20 tokens would become permanently stuck. Administrators can withdraw funds using dedicated withdrawal functions.

#### C. Direct Token Querying
*   **Target**: `QueryMsg::TestApprovedTokens` (superseding local state queries)
*   **Logic**:
    *   Directly queries the Chain API `ApprovedTokensForTrade` (which automatically returns *both* lists due to the shared state in `LiquidityPoolApprovedTokensMap`).
    *   Returns the unmerged raw list directly to the UI for consumption alongside dynamic rate querying.

### 3. Contract Logic (`community-sale`)

#### A. IBC Payment Support
*   **Method**: `ExecuteMsg::PurchaseWithNative`
*   **Logic**: Added support for purchasing GNK using native IBC tokens (e.g., Nobel USDC). The contract validates the IBC denom against the governance-maintained allowlist on the chain.

#### B. Dynamic Multi-Token Support (`allow_all_trade_tokens`)
*   **Method**: `ExecuteMsg::UpdateAllowAllTradeTokens` & `Config` structural changes.
*   **Logic**: Introduced an `allow_all_trade_tokens` configuration boolean. When enabled, the contract bypasses checks for a *single* strictly defined `accepted_ibc_denom` or `accepted_eth_contract`. Instead, it mirrors the `liquidity-pool` behavior by independently validating *any* received token dynamically against the on-chain governance allowlist (via `ValidateWrappedTokenForTrade` and `ValidateIbcTokenForTrade` queries).

#### C. Migration Support
*   **Migration Path**: Implemented a comprehensive `migrate` entry point that allows existing contract instances (like the one funded in Proposal 14) to be safely upgraded to this new version.
*   **Config Backwards-Compatibility**: If no V3 configuration updates are passed during migration, the contract perfectly retains the pre-migration restrictions (falling back to requiring the explicitly defined single token configurations) preserving the expected behavior of any active legacy contracts.

---

## Bridge Audit Fixes Included in This PR

The following security findings from Certik bridge audit have been resolved as part of this change set:

| ID | Severity | Title | Fixed In |
|----|----------|-------|----------|
| GEB-05 | Medium | Native Denom Auto-Detection Can Be Misconfigured in `community-sale` Contract | `community-sale/src/contract.rs` |
| GEB-07 | Minor | Weak Address Validation in `withdraw()` in `wrapped-token` Contract | `contracts/wrapped-token/src/contract.rs` |
| GEB-11 | Info | `InstantiateMsg.marketing` Is Ignored | `contracts/wrapped-token/src/contract.rs` |
| GEB-15 | Medium | Cross-Chain Address Collision in `IsBridgeContractAddress()` | `keeper/bridge_native.go` |
| GEB-36 | Medium | Authority Mismatch In `MigrateAllWrappedTokenContracts()` | `keeper/bridge_wrapped_token.go` |
| GEB-39 | Minor | Missing Validation of `msg.Amount` Being Positive in `MsgRequestBridgeWithdrawal` | `types/message_request_bridge_withdrawal.go` |

### Summary of Fixes

- **GEB-05**: Native denom is now passed explicitly as an instantiation parameter instead of being auto-detected from bank supply, preventing manipulation attacks.
- **GEB-07**: `withdraw()` now validates that `destination_address` conforms to the Ethereum address format (0x + 40 hex chars) to prevent permanent fund loss from invalid destinations.
- **GEB-11**: Removed the unused `marketing` field from `InstantiateMsg` — marketing info is hardcoded and governed on-chain via the governance module.
- **GEB-15**: `IsBridgeContractAddress()` now takes `chainId` as a parameter and checks the exact `(chainId, address)` pair, preventing cross-chain address collisions with CREATE2/deterministic deployments.
- **GEB-36**: `MigrateAllWrappedTokenContracts()` now correctly uses the governance address (matching how contracts were instantiated) instead of the inference module address, resolving the authorization mismatch that caused DoS.
- **GEB-39**: `MsgRequestBridgeWithdrawal.ValidateBasic()` now parses and validates that `Amount` is a positive integer using `math.NewIntFromString`, rejecting zero, negative, and non-numeric values.

