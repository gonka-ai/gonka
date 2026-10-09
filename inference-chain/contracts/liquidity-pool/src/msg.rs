use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::{Binary, Coin, Uint256};
use std::collections::HashMap;

#[cw_serde]
pub struct InstantiateMsg {
    /// Optional admin address that can pause/unpause and update config. If None, contract is governance-only.
    pub admin: Option<String>,
    /// Daily selling limit in basis points (1-10000, where 10000 = 100%)
    pub daily_limit_bp: Option<Uint256>,
    /// Optional base price per token in USD (with 6 decimals for USD, so 25000 = $0.025). Defaults to 25000.
    pub base_price_usd: Option<Uint256>,
    /// Optional tokens per tier with 9 decimals (default: 3_000_000_000_000_000 for 3 million tokens)
    pub tokens_per_tier: Option<Uint256>,
    /// Optional price multiplier for each tier (1300 = 1.3x, default: 1300)
    pub tier_multiplier: Option<Uint256>,
    /// Initial total supply of native tokens (defaults to 0 if not provided)
    pub total_supply: Option<Uint256>,
    /// Optional native token denomination (defaults to "ngonka" if not provided)
    pub native_denom: Option<String>,
}

#[cw_serde]
pub enum ExecuteMsg {
    /// Receive CW20 wrapped bridge tokens to purchase native tokens
    Receive(Cw20ReceiveMsg),
    /// Purchase native tokens with native coins (identifiers parsed from message funds)
    PurchaseWithNative {},
    /// Admin: Pause the contract
    Pause {},
    /// Admin: Resume the contract
    Resume {},
    /// Admin: Update daily limit in basis points
    UpdateDailyLimit { daily_limit_bp: Option<Uint256> },
    /// Admin: Withdraw native tokens (Gonka) from contract
    WithdrawNative { amount: Uint256, recipient: String },
    /// Admin: Withdraw IBC/other native tokens (e.g. USDC)
    WithdrawIbc {
        denom: String,
        amount: Uint256,
        recipient: String,
    },
    /// Admin: Withdraw CW20 tokens
    WithdrawCw20 {
        contract_addr: String,
        amount: Uint256,
        recipient: String,
    },
    /// Admin: Emergency withdraw all funds
    EmergencyWithdraw { recipient: String },
    /// Admin: Update pricing configuration
    UpdatePricingConfig {
        base_price_usd: Option<Uint256>,
        tokens_per_tier: Option<Uint256>,
        tier_multiplier: Option<Uint256>,
    },
}

#[cw_serde]
pub struct Cw20ReceiveMsg {
    pub sender: String,
    pub amount: Uint256,
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
    CalculateTokens { usd_amount: Uint256 },
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
    pub daily_limit_bp: Uint256,
    pub is_paused: bool,
    pub total_tokens_sold: Uint256,
}

#[cw_serde]
pub struct DailyStatsResponse {
    pub current_day: u64,
    pub usd_received_today: Uint256,
    pub tokens_sold_today: Uint256,
    pub tokens_available_today: Uint256,
    pub daily_token_limit: Uint256,
    pub total_supply: Uint256,
}

#[cw_serde]
pub struct AcceptedTokensResponse {
    pub tokens: HashMap<String, Uint256>,
}

#[cw_serde]
pub struct NativeBalanceResponse {
    pub balance: Coin,
}

#[cw_serde]
pub struct PricingInfoResponse {
    pub current_tier: u32,
    pub current_price_usd: Uint256,
    pub total_tokens_sold: Uint256,
    pub tokens_per_tier: Uint256,
    pub base_price_usd: Uint256,
    pub tier_multiplier: Uint256,
    pub next_tier_at: Uint256,
    pub next_tier_price: Uint256,
}

#[cw_serde]
pub struct TokenCalculationResponse {
    pub tokens: Uint256,
    pub current_price: Uint256,
    pub current_tier: u32,
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
