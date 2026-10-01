use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::{Binary, StdError, StdResult, Uint256};
use cw_utils::Expiration;

use super::logo::Logo;
use super::types::{
    AllAccountsResponse, AllAllowancesResponse, AllSpenderAllowancesResponse, AllowanceResponse,
    BalanceResponse, Cw20Coin, DownloadLogoResponse, MarketingInfoResponse, MinterResponse,
    TokenInfoResponse,
};

#[cw_serde]
pub enum ExecuteMsg {
    Transfer {
        recipient: String,
        amount: Uint256,
    },
    Burn {
        amount: Uint256,
    },
    Send {
        contract: String,
        amount: Uint256,
        msg: Binary,
    },
    IncreaseAllowance {
        spender: String,
        amount: Uint256,
        expires: Option<Expiration>,
    },
    DecreaseAllowance {
        spender: String,
        amount: Uint256,
        expires: Option<Expiration>,
    },
    TransferFrom {
        owner: String,
        recipient: String,
        amount: Uint256,
    },
    SendFrom {
        owner: String,
        contract: String,
        amount: Uint256,
        msg: Binary,
    },
    BurnFrom {
        owner: String,
        amount: Uint256,
    },
    Mint {
        recipient: String,
        amount: Uint256,
    },
    UpdateMinter {
        new_minter: Option<String>,
    },
    UpdateMarketing {
        project: Option<String>,
        description: Option<String>,
        marketing: Option<String>,
    },
    UploadLogo(Logo),
}

#[cw_serde]
pub struct InstantiateMarketingInfo {
    pub project: Option<String>,
    pub description: Option<String>,
    pub marketing: Option<String>,
    pub logo: Option<Logo>,
}

#[cw_serde]
#[cfg_attr(test, derive(Default))]
pub struct InstantiateMsg {
    pub name: String,
    pub symbol: String,
    pub decimals: u8,
    pub initial_balances: Vec<Cw20Coin>,
    pub mint: Option<MinterResponse>,
    pub marketing: Option<InstantiateMarketingInfo>,
}

impl InstantiateMsg {
    pub fn get_cap(&self) -> Option<Uint256> {
        self.mint.as_ref().and_then(|v| v.cap)
    }

    pub fn validate(&self) -> StdResult<()> {
        if !self.has_valid_name() {
            return Err(StdError::generic_err(
                "Name is not in the expected format (3-50 UTF-8 bytes)",
            ));
        }
        if !self.has_valid_symbol() {
            return Err(StdError::generic_err(
                "Ticker symbol is not in expected format [a-zA-Z\\-]{3,12}",
            ));
        }
        if self.decimals > 18 {
            return Err(StdError::generic_err("Decimals must not exceed 18"));
        }
        Ok(())
    }

    fn has_valid_name(&self) -> bool {
        let bytes = self.name.as_bytes();
        bytes.len() >= 3 && bytes.len() <= 50
    }

    fn has_valid_symbol(&self) -> bool {
        let bytes = self.symbol.as_bytes();
        if !(3..=12).contains(&bytes.len()) {
            return false;
        }
        for byte in bytes.iter() {
            if (*byte != 45) && (*byte < 65 || *byte > 90) && (*byte < 97 || *byte > 122) {
                return false;
            }
        }
        true
    }
}

#[cw_serde]
#[derive(QueryResponses)]
pub enum QueryMsg {
    #[returns(BalanceResponse)]
    Balance { address: String },
    #[returns(TokenInfoResponse)]
    TokenInfo {},
    #[returns(MinterResponse)]
    Minter {},
    #[returns(AllowanceResponse)]
    Allowance { owner: String, spender: String },
    #[returns(AllAllowancesResponse)]
    AllAllowances {
        owner: String,
        start_after: Option<String>,
        limit: Option<u32>,
    },
    #[returns(AllSpenderAllowancesResponse)]
    AllSpenderAllowances {
        spender: String,
        start_after: Option<String>,
        limit: Option<u32>,
    },
    #[returns(AllAccountsResponse)]
    AllAccounts {
        start_after: Option<String>,
        limit: Option<u32>,
    },
    #[returns(MarketingInfoResponse)]
    MarketingInfo {},
    #[returns(DownloadLogoResponse)]
    DownloadLogo {},
}

#[cw_serde]
pub struct MigrateMsg {}
