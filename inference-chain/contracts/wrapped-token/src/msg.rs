use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::{Binary, Uint256};

#[cw_serde]
pub struct InstantiateMsg {
    /// Chain ID where the original token exists
    pub chain_id: String,
    /// Original contract address on the external chain
    pub contract_address: String,
    /// Initial balances to set for the wrapped token (usually empty)
    pub initial_balances: Vec<Cw20Coin>,
    /// Optional minter, if unset only the instantiating address can mint
    pub mint: Option<MinterResponse>,
    /// Optional admin address (WASM admin = governance module). If not provided, will try to query from contract info.
    pub admin: Option<String>,
}

#[cw_serde]
pub struct Cw20Coin {
    pub address: String,
    pub amount: Uint256,
}

#[cw_serde]
pub struct MinterResponse {
    pub minter: String,
    pub cap: Option<Uint256>,
}

#[cw_serde]
pub enum Logo {
    Url(String),
    Embedded(EmbeddedLogo),
}

#[cw_serde]
pub enum EmbeddedLogo {
    Svg(Binary),
    Png(Binary),
}

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
    Withdraw {
        amount: Uint256,
        destination_address: String,
        destination_bridge_address: String,
    },
    UpdateMetadata {
        name: String,
        symbol: String,
        decimals: u8,
    },
    UpdateMarketing {
        project: Option<String>,
        description: Option<String>,
        marketing: Option<String>,
    },
    UploadLogo(Logo),
}

#[cw_serde]
pub enum Expiration {
    AtHeight(u64),
    AtTime(cosmwasm_std::Timestamp),
    Never {},
}

impl Expiration {
    pub fn is_expired(&self, block: &cosmwasm_std::BlockInfo) -> bool {
        match self {
            Expiration::AtHeight(height) => block.height >= *height,
            Expiration::AtTime(time) => block.time >= *time,
            Expiration::Never {} => false,
        }
    }
}

#[cw_serde]
#[derive(QueryResponses)]
pub enum QueryMsg {
    #[returns(BalanceResponse)]
    Balance { address: String },
    #[returns(TokenInfoResponse)]
    TokenInfo {},
    #[returns(BridgeInfoResponse)]
    BridgeInfo {},
    #[returns(AllowanceResponse)]
    Allowance { owner: String, spender: String },
    #[returns(AllAllowancesResponse)]
    AllAllowances {
        owner: String,
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
    #[returns(MinterResponse)]
    Minter {},
    #[returns(ApprovedTokensForTradeJson)]
    TestApprovedTokens {},
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

#[cw_serde]
pub struct BridgeInfoResponse {
    pub chain_id: String,
    pub contract_address: String,
}

#[cw_serde]
pub struct AllowanceResponse {
    pub allowance: Uint256,
    pub expires: Expiration,
}

#[cw_serde]
pub struct AllowanceInfo {
    pub spender: String,
    pub allowance: Uint256,
    pub expires: Expiration,
}

#[cw_serde]
pub struct AllAllowancesResponse {
    pub allowances: Vec<AllowanceInfo>,
}

#[cw_serde]
pub struct AllAccountsResponse {
    pub accounts: Vec<String>,
}

#[cw_serde]
pub struct MarketingInfoResponse {
    pub project: Option<String>,
    pub description: Option<String>,
    pub marketing: Option<String>,
    pub logo: Option<LogoInfo>,
}

#[cw_serde]
pub enum LogoInfo {
    Url(String),
    Embedded,
}

#[cw_serde]
pub struct DownloadLogoResponse {
    pub mime_type: String,
    pub data: Binary,
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

#[cw_serde]
pub struct Cw20ReceiveMsg {
    pub sender: String,
    pub amount: Uint256,
    pub msg: Binary,
}
