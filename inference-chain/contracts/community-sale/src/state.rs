use cosmwasm_schema::cw_serde;
use cosmwasm_std::Uint128;
use cw_storage_plus::Item;

#[cw_serde]
pub struct Config {
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
    /// Native token denomination
    pub native_denom: String,
    /// Whether contract is paused
    pub is_paused: bool,
    /// Total tokens sold
    pub total_tokens_sold: Uint128,
}

/// Contract configuration
pub const CONFIG: Item<Config> = Item::new("config");

/// Calculate how many tokens can be bought with given USD amount at fixed price
pub fn calculate_tokens_for_usd(usd_amount: Uint128, price_per_token: Uint128) -> Uint128 {
    if price_per_token.is_zero() {
        return Uint128::zero();
    }
    // usd_amount has 6 decimals, price_per_token has 6 decimals
    // Result should be in token units (9 decimals)
    // Scale by 1e9 to get 9-decimal tokens
    usd_amount
        .checked_mul(Uint128::from(1_000_000_000u128))
        .unwrap_or(Uint128::zero())
        .checked_div(price_per_token)
        .unwrap_or(Uint128::zero())
}
