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
    /// Total tokens sold across all tiers (used for pricing tier calculation)
    pub total_tokens_sold: Uint128,
}

#[cw_serde]
pub struct DailyStats {
    /// Current day (block time / 86400)
    pub current_day: u64,
    /// USD amount received today (for tracking)
    pub usd_received_today: Uint128,
    /// Token amount sold today (for daily limits)
    pub tokens_sold_today: Uint128,
}

#[cw_serde]
pub struct PricingConfig {
    /// Base price per token in USD (with 6 decimals for USD, so 25000 = $0.025)
    pub base_price_usd: Uint128,
    /// Tokens per tier with 9 decimals (3 million = 3_000_000_000_000_000)
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

/// Calculate current price per token in USD (6 decimals for USD)
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
    // Result should be in token units (9 decimals)
    // Scale by 1e9 to get 9-decimal tokens
    usd_amount
        .checked_mul(Uint128::from(1_000_000_000u128)) // 1e9 for 9-decimal tokens
        .unwrap_or(Uint128::zero())
        .checked_div(price_per_token)
        .unwrap_or(Uint128::zero())
}

/// Calculate multi-tier purchase: handles purchases that span multiple pricing tiers
/// Returns (total_tokens_to_buy, actual_usd_spent, start_tier, end_tier, average_price_paid)
pub fn calculate_multi_tier_purchase(
    usd_amount: Uint128,
    current_tokens_sold: Uint128,
    pricing_config: &PricingConfig,
) -> (Uint128, Uint128, u32, u32, Uint128) {
    if usd_amount.is_zero() || pricing_config.tokens_per_tier.is_zero() || pricing_config.base_price_usd.is_zero() {
        return (Uint128::zero(), Uint128::zero(), 0, 0, Uint128::zero());
    }

    let mut remaining_usd = usd_amount;
    let mut total_tokens = Uint128::zero();
    let mut current_tokens_sold_so_far = current_tokens_sold;
    let mut actual_usd_spent = Uint128::zero();
    
    // Track tier progression
    let start_tier = calculate_current_tier(current_tokens_sold, pricing_config.tokens_per_tier);
    let mut end_tier = start_tier;

    // Maximum 50 tier iterations to prevent infinite loops in case of edge cases
    for iteration in 0..50 {
        if remaining_usd.is_zero() {
            break;
        }

        // Calculate current tier based on tokens sold so far
        let current_tier = calculate_current_tier(current_tokens_sold_so_far, pricing_config.tokens_per_tier);
        
        // Calculate tier progression
        
        // Calculate current price for this tier
        let current_price = calculate_current_price(
            pricing_config.base_price_usd,
            current_tier,
            pricing_config.tier_multiplier,
        );

        if current_price.is_zero() {
            break;
        }

        // How many tokens are left in the current tier?
        let tokens_already_sold_in_tier = current_tokens_sold_so_far
            .checked_rem(pricing_config.tokens_per_tier)
            .unwrap_or_default();
        let tokens_left_in_tier = pricing_config.tokens_per_tier
            .checked_sub(tokens_already_sold_in_tier)
            .unwrap_or_default();

        // How much USD is needed to buy all remaining tokens in this tier?
        // tokens_left_in_tier has 9 decimals, current_price has 6 decimals
        // We need to divide by 1e9 to get the correct USD amount with 6 decimals
        let usd_for_remaining_tier = tokens_left_in_tier
            .checked_mul(current_price)
            .unwrap_or_default()
            .checked_div(Uint128::from(1_000_000_000u128))
            .unwrap_or_default();

        // Calculate USD needed and spending strategy

        // How much USD will we spend in this tier?
        let usd_to_spend_in_tier = if remaining_usd <= usd_for_remaining_tier {
            remaining_usd // We can complete the purchase in this tier
        } else {
            usd_for_remaining_tier // We need to buy the rest of this tier and continue
        };

        if usd_to_spend_in_tier.is_zero() {
            break;
        }

        // Calculate tokens for this tier portion
        let tokens_in_tier = calculate_tokens_for_usd(usd_to_spend_in_tier, current_price);
        
        // Update running totals
        total_tokens = total_tokens.checked_add(tokens_in_tier).unwrap_or(total_tokens);
        actual_usd_spent = actual_usd_spent.checked_add(usd_to_spend_in_tier).unwrap_or(actual_usd_spent);
        remaining_usd = remaining_usd.checked_sub(usd_to_spend_in_tier).unwrap_or_default();
        current_tokens_sold_so_far = current_tokens_sold_so_far.checked_add(tokens_in_tier).unwrap_or(current_tokens_sold_so_far);
        
        // Update end tier
        end_tier = calculate_current_tier(current_tokens_sold_so_far, pricing_config.tokens_per_tier);
    }

    // Calculate average price paid (USD per token)
    // USD has 6 decimals, tokens have 9 decimals, we want price in 6-decimal USD format
    let average_price = if total_tokens.is_zero() {
        Uint128::zero()
    } else {
        // Scale up USD by 1e9 to match token decimals, then divide by tokens
        // This gives us price in micro-USD per token (same as base_price format)
        actual_usd_spent
            .checked_mul(Uint128::from(1_000_000_000u128))
            .unwrap_or_default()
            .checked_div(total_tokens)
            .unwrap_or_default()
    };

    (total_tokens, actual_usd_spent, start_tier, end_tier, average_price)
} 