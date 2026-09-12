//! Bridge-specific storage only.
//! Token balances / supply / allowances live in `base::state` (cw20-base keys).

use cosmwasm_schema::cw_serde;
use cosmwasm_std::Addr;
use cw_storage_plus::Item;

#[cw_serde]
pub struct BridgeInfo {
    /// Original chain ID where the token exists
    pub chain_id: String,
    /// Original contract address on the external chain
    pub contract_address: String,
}

pub const BRIDGE_INFO: Item<BridgeInfo> = Item::new("bridge_info");

/// Optional metadata override that can be updated post-instantiate by admin
#[cw_serde]
pub struct TokenMetadataOverride {
    pub name: String,
    pub symbol: String,
    pub decimals: u8,
}

pub const TOKEN_METADATA: Item<TokenMetadataOverride> = Item::new("token_metadata");

/// Creator = inference module (minter / withdraw signer path)
pub const CREATOR: Item<Addr> = Item::new("creator");
