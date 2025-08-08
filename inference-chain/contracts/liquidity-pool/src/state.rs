use cosmwasm_schema::cw_serde;
use cosmwasm_std::Uint128;
use cw_storage_plus::Item;

#[cw_serde]
pub struct Config {
    /// Admin address
    pub admin: String,
    /// Native token denomination
    pub native_denom: String,
    /// Daily selling limit in basis points (1-10000)
    pub daily_limit_bp: Uint128,
    /// Whether contract is paused
    pub is_paused: bool,
    /// Total supply of native tokens allocated to this contract
    pub total_supply: Uint128,
    /// Total tokens sold across all tiers
    pub total_sold: Uint128,
}

#[cw_serde]
pub struct DailyStats {
    /// Current day (block time / 86400)
    pub current_day: u64,
    /// Amount sold today
    pub sold_today: Uint128,
}

#[cw_serde]
pub struct PricingConfig {
    /// Base price per token in USD (with 6 decimals, so 25000 = $0.025)
    pub base_price_usd: Uint128,
    /// Tokens per tier (10 million = 10_000_000)
    pub tokens_per_tier: Uint128,
    /// Price multiplier for each tier (1.3x = 1300, representing 1300/1000)
    pub tier_multiplier: Uint128,
}

/// Contract configuration
pub const CONFIG: Item<Config> = Item::new("config");

/// Daily selling statistics
pub const DAILY_STATS: Item<DailyStats> = Item::new("daily_stats");

/// Pricing configuration for tiered pricing
pub const PRICING_CONFIG: Item<PricingConfig> = Item::new("pricing_config");

/// Calculate current tier based on tokens sold
pub fn calculate_current_tier(tokens_sold: Uint128, tokens_per_tier: Uint128) -> u32 {
    if tokens_per_tier.is_zero() {
        return 0;
    }
    (tokens_sold / tokens_per_tier).u128() as u32
}

/// Calculate current tier based on USD value sold
pub fn calculate_current_tier_usd(usd_sold: Uint128, tokens_per_tier: Uint128, base_price: Uint128) -> u32 {
    if tokens_per_tier.is_zero() || base_price.is_zero() {
        return 0;
    }
    // Calculate how much USD is needed for one tier
    let usd_per_tier = tokens_per_tier.checked_mul(base_price).unwrap_or_default();
    if usd_per_tier.is_zero() {
        return 0;
    }
    (usd_sold / usd_per_tier).u128() as u32
}

/// Calculate current price per token in USD (6 decimals)
pub fn calculate_current_price(
    base_price: Uint128,
    current_tier: u32,
    tier_multiplier: Uint128,
) -> Uint128 {
    let mut price = base_price;
    for _ in 0..current_tier {
        price = price
            .checked_mul(tier_multiplier)
            .unwrap_or(price)
            .checked_div(Uint128::from(1000u128))
            .unwrap_or(price);
    }
    price
}

/// Calculate how many tokens can be bought with given USD amount
pub fn calculate_tokens_for_usd(
    usd_amount: Uint128,
    price_per_token: Uint128,
) -> Uint128 {
    if price_per_token.is_zero() {
        return Uint128::zero();
    }
    // usd_amount has 6 decimals, price_per_token has 6 decimals
    // Result should be in token units (6 decimals)
    usd_amount
        .checked_mul(Uint128::from(1_000_000u128))
        .unwrap_or(Uint128::zero())
        .checked_div(price_per_token)
        .unwrap_or(Uint128::zero())
} 