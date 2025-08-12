use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::{Binary, Coin, Uint128};
use std::collections::HashMap;

#[cw_serde]
pub struct InstantiateMsg {
    /// Optional admin address that can pause/unpause and update config. If None, contract is governance-only.
    pub admin: Option<String>,
    /// Daily selling limit in basis points (1-10000, where 10000 = 100%)
    pub daily_limit_bp: Option<Uint128>,
    /// Optional base price per token in USD (with 6 decimals for USD, so 25000 = $0.025). Defaults to 25000.
    pub base_price_usd: Option<Uint128>,
    /// Optional tokens per tier with 9 decimals (default: 3_000_000_000_000_000 for 3 million tokens)
    pub tokens_per_tier: Option<Uint128>,
    /// Optional price multiplier for each tier (1300 = 1.3x, default: 1300)
    pub tier_multiplier: Option<Uint128>,
    /// Initial total supply of native tokens (defaults to 0 if not provided)
    pub total_supply: Option<Uint128>,
}

#[cw_serde]
pub enum ExecuteMsg {
    /// Receive CW20 wrapped bridge tokens to purchase native tokens
    Receive(Cw20ReceiveMsg),
    /// Admin: Pause the contract
    Pause {},
    /// Admin: Resume the contract
    Resume {},
    /// Admin: Update daily limit in basis points
    UpdateDailyLimit { daily_limit_bp: Option<Uint128> },
    /// Admin: Withdraw native tokens from contract
    WithdrawNativeTokens { amount: Uint128, recipient: String },
    /// Admin: Emergency withdraw all funds
    EmergencyWithdraw { recipient: String },
    /// Admin: Update pricing configuration
    UpdatePricingConfig {
        base_price_usd: Option<Uint128>,
        tokens_per_tier: Option<Uint128>,
        tier_multiplier: Option<Uint128>,
    },
    /// Admin: Add or update a payment token and its USD rate
    AddPaymentToken { 
        denom: String, 
        usd_rate: Uint128 // micro-USD per token unit
    },
    /// Admin: Remove a payment token
    RemovePaymentToken { denom: String },
}

#[cw_serde]
pub struct Cw20ReceiveMsg {
    pub sender: String,
    pub amount: Uint128,
    pub msg: Binary,
}

#[cw_serde]
pub struct PurchaseTokenMsg {
    // Empty for now, could add recipient address later
}

#[cw_serde]
#[derive(QueryResponses)]
pub enum QueryMsg {
    /// Get contract configuration
    #[returns(ConfigResponse)]
    Config {},
    /// Get current daily statistics
    #[returns(DailyStatsResponse)]
    DailyStats {},
    /// Get contract's native token balance
    #[returns(NativeBalanceResponse)]
    NativeBalance {},
    /// Get current pricing information
    #[returns(PricingInfoResponse)]
    PricingInfo {},
    /// Calculate how many tokens can be bought with given USD amount
    #[returns(TokenCalculationResponse)]
    CalculateTokens { usd_amount: Uint128 },
    /// Test bridge validation with a provided CW20 contract address
    #[returns(TestBridgeValidationResponse)]
    TestBridgeValidation { cw20_contract: String },
    /// Return the current block height
    #[returns(BlockHeightResponse)]
    BlockHeight {},
    /// Test gRPC call to fetch approved tokens for trade; returns raw protobuf bytes
    #[returns(ApprovedTokensForTradeJson)]
    TestApprovedTokens {},
}

#[cw_serde]
pub struct ConfigResponse {
    pub admin: String,
    pub native_denom: String,
    pub daily_limit_bp: Uint128,
    pub is_paused: bool,
    pub total_tokens_sold: Uint128,
}

#[cw_serde]
pub struct DailyStatsResponse {
    pub current_day: u64,
    pub usd_received_today: Uint128,
    pub tokens_sold_today: Uint128,
    pub tokens_available_today: Uint128,
    pub daily_token_limit: Uint128,
    pub total_supply: Uint128,
}

#[cw_serde]
pub struct AcceptedTokensResponse {
    pub tokens: HashMap<String, Uint128>,
}

#[cw_serde]
pub struct NativeBalanceResponse {
    pub balance: Coin,
}

#[cw_serde]
pub struct PricingInfoResponse {
    pub current_tier: u32,
    pub current_price_usd: Uint128,
    pub total_tokens_sold: Uint128,
    pub tokens_per_tier: Uint128,
    pub base_price_usd: Uint128,
    pub tier_multiplier: Uint128,
    pub next_tier_at: Uint128,
    pub next_tier_price: Uint128,
}

#[cw_serde]
pub struct TokenCalculationResponse {
    pub tokens: Uint128,
    pub current_price: Uint128,
    pub current_tier: u32,
}

#[cw_serde]
pub struct PaymentTokensResponse {
    pub tokens: HashMap<String, Uint128>, // denom -> USD rate
} 

#[cw_serde]
pub struct TestBridgeValidationResponse {
    pub is_valid: bool,
}

#[cw_serde]
pub struct BlockHeightResponse {
    pub height: u64,
}

// JSON-normalized response for ApprovedTokensForTrade
#[cw_serde]
pub struct ApprovedTokensForTradeJson {
    pub approved_tokens: Vec<ApprovedTokenJson>,
}

#[cw_serde]
pub struct ApprovedTokenJson {
    pub chain_id: String,
    pub contract_address: String,
}