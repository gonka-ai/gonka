use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::{Binary, Coin, Uint128};

#[cw_serde]
pub struct InstantiateMsg {
    /// Admin address (governance module - receives W(USDT), can withdraw unsold tokens)
    pub admin: String,
    /// Designated buyer address (only address allowed to purchase)
    pub buyer: String,
    /// Accepted chain ID (e.g., "ethereum")
    pub accepted_chain_id: String,
    /// Accepted contract address on external chain (e.g., "0xdac17f958d2ee523a2206206994597c13d831ec7" for USDT)
    pub accepted_eth_contract: String,
    /// Fixed price per 1 GNK in micro-USD (6 decimals, e.g., 25000 = $0.025/GNK)
    pub price_usd: Uint128,
}

#[cw_serde]
pub enum ExecuteMsg {
    /// Receive CW20 wrapped bridge tokens to purchase native tokens
    Receive(Cw20ReceiveMsg),
    /// Admin: Pause the contract
    Pause {},
    /// Admin: Resume the contract
    Resume {},
    /// Admin: Update buyer address
    UpdateBuyer { buyer: String },
    /// Admin: Update fixed price
    UpdatePrice { price_usd: Uint128 },
    /// Admin: Withdraw native tokens from contract
    WithdrawNativeTokens { amount: Uint128, recipient: String },
    /// Admin: Emergency withdraw all funds
    EmergencyWithdraw { recipient: String },
}

#[cw_serde]
pub struct Cw20ReceiveMsg {
    pub sender: String,
    pub amount: Uint128,
    pub msg: Binary,
}

#[cw_serde]
pub struct PurchaseTokenMsg {}

#[cw_serde]
#[derive(QueryResponses)]
pub enum QueryMsg {
    /// Get contract configuration
    #[returns(ConfigResponse)]
    Config {},
    /// Get contract's native token balance
    #[returns(NativeBalanceResponse)]
    NativeBalance {},
    /// Calculate how many tokens can be bought with given USD amount
    #[returns(TokenCalculationResponse)]
    CalculateTokens { usd_amount: Uint128 },
    /// Test bridge validation with a provided CW20 contract address
    #[returns(TestBridgeValidationResponse)]
    TestBridgeValidation { cw20_contract: String },
    /// Return the current block height
    #[returns(BlockHeightResponse)]
    BlockHeight {},
    /// Test gRPC call to fetch approved tokens for trade
    #[returns(ApprovedTokensForTradeJson)]
    TestApprovedTokens {},
}

#[cw_serde]
pub struct ConfigResponse {
    pub admin: String,
    pub buyer: String,
    pub accepted_chain_id: String,
    pub accepted_eth_contract: String,
    pub price_usd: Uint128,
    pub native_denom: String,
    pub is_paused: bool,
    pub total_tokens_sold: Uint128,
}

#[cw_serde]
pub struct NativeBalanceResponse {
    pub balance: Coin,
}

#[cw_serde]
pub struct TokenCalculationResponse {
    pub tokens: Uint128,
    pub price_usd: Uint128,
}

#[cw_serde]
pub struct TestBridgeValidationResponse {
    pub is_valid: bool,
}

#[cw_serde]
pub struct BlockHeightResponse {
    pub height: u64,
}

#[cw_serde]
pub struct ApprovedTokensForTradeJson {
    pub approved_tokens: Vec<ApprovedTokenJson>,
}

#[cw_serde]
pub struct ApprovedTokenJson {
    pub chain_id: String,
    pub contract_address: String,
}
