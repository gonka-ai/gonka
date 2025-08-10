use cosmwasm_schema::cw_serde;
use cosmwasm_std::{Addr, Uint128};
use cw_storage_plus::{Item, Map};

use crate::msg::{Expiration, Logo, MinterResponse};

#[cw_serde]
pub struct TokenInfo {
    pub name: String,
    pub symbol: String,
    pub decimals: u8,
    pub total_supply: Uint128,
    pub mint: Option<MinterResponse>,
}

#[cw_serde]
pub struct BridgeInfo {
    /// Original chain ID where the token exists
    pub chain_id: String,
    /// Original contract address on the external chain
    pub contract_address: String,
}

#[cw_serde]
pub struct MarketingInfo {
    pub project: Option<String>,
    pub description: Option<String>,
    pub marketing: Option<Addr>,
    pub logo: Option<Logo>,
}

pub const TOKEN_INFO: Item<TokenInfo> = Item::new("token_info");
pub const BRIDGE_INFO: Item<BridgeInfo> = Item::new("bridge_info");
pub const MARKETING_INFO: Item<MarketingInfo> = Item::new("marketing_info");
pub const LOGO: Item<Logo> = Item::new("logo");
pub const BALANCES: Map<&Addr, Uint128> = Map::new("balance");
pub const ALLOWANCES: Map<(&Addr, &Addr), AllowanceResponse> = Map::new("allowance");

// Optional metadata override that can be updated post-instantiate by admin
#[cw_serde]
pub struct TokenMetadataOverride {
    pub name: String,
    pub symbol: String,
    pub decimals: u8,
}

pub const TOKEN_METADATA: Item<TokenMetadataOverride> = Item::new("token_metadata");

#[cw_serde]
pub struct AllowanceResponse {
    pub allowance: Uint128,
    pub expires: Expiration,
}

impl AllowanceResponse {
    pub fn is_expired(&self, block: &cosmwasm_std::BlockInfo) -> bool {
        self.expires.is_expired(block)
    }
}