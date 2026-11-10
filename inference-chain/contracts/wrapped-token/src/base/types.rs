use cosmwasm_schema::cw_serde;
use cosmwasm_std::{Addr, Binary, Uint256};
use cw_utils::Expiration;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use std::fmt;

pub use super::logo::{EmbeddedLogo, Logo, LogoInfo};

#[cw_serde]
pub struct Cw20Coin {
    pub address: String,
    pub amount: Uint256,
}

impl Cw20Coin {
    pub fn is_empty(&self) -> bool {
        self.amount.is_zero()
    }
}

impl fmt::Display for Cw20Coin {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        write!(f, "address: {}, amount: {}", self.address, self.amount)
    }
}

#[cw_serde]
pub struct BalanceResponse {
    pub balance: Uint256,
}

#[cw_serde]
pub struct TokenInfoResponse {
    pub name: String,
    pub symbol: String,
    pub decimals: u8,
    pub total_supply: Uint256,
}

#[derive(Serialize, Deserialize, Clone, PartialEq, JsonSchema, Debug, Default)]
pub struct AllowanceResponse {
    pub allowance: Uint256,
    pub expires: Expiration,
}

#[cw_serde]
pub struct MinterResponse {
    pub minter: String,
    pub cap: Option<Uint256>,
}

#[derive(Serialize, Deserialize, Clone, PartialEq, JsonSchema, Debug, Default)]
pub struct MarketingInfoResponse {
    pub project: Option<String>,
    pub description: Option<String>,
    pub logo: Option<LogoInfo>,
    pub marketing: Option<Addr>,
}

#[cw_serde]
pub struct DownloadLogoResponse {
    pub mime_type: String,
    pub data: Binary,
}

#[cw_serde]
pub struct AllowanceInfo {
    pub spender: String,
    pub allowance: Uint256,
    pub expires: Expiration,
}

#[derive(Serialize, Deserialize, Clone, PartialEq, JsonSchema, Debug, Default)]
pub struct AllAllowancesResponse {
    pub allowances: Vec<AllowanceInfo>,
}

#[cw_serde]
pub struct SpenderAllowanceInfo {
    pub owner: String,
    pub allowance: Uint256,
    pub expires: Expiration,
}

#[derive(Serialize, Deserialize, Clone, PartialEq, JsonSchema, Debug, Default)]
pub struct AllSpenderAllowancesResponse {
    pub allowances: Vec<SpenderAllowanceInfo>,
}

#[derive(Serialize, Deserialize, Clone, PartialEq, Eq, JsonSchema, Debug, Default)]
pub struct AllAccountsResponse {
    pub accounts: Vec<String>,
}
