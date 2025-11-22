use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::{Binary, Uint128};

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
    /// Optional marketing data
    pub marketing: Option<InstantiateMarketingInfo>,
    /// Optional admin address (WASM admin = governance module). If not provided, will try to query from contract info.
    pub admin: Option<String>,
}

#[cw_serde]
pub struct Cw20Coin {
    pub address: String,
    pub amount: Uint128,
}

#[cw_serde]
pub struct MinterResponse {
    pub minter: String,
    pub cap: Option<Uint128>,
}

#[cw_serde]
pub struct InstantiateMarketingInfo {
    pub project: Option<String>,
    pub description: Option<String>,
    pub marketing: Option<String>,
    pub logo: Option<Logo>,
}

#[cw_serde]
pub enum Logo {
    /// A reference to an externally hosted logo. Must be a valid HTTP or HTTPS URL.
    Url(String),
    /// Logo content stored on the blockchain. Enforce maximum size of 5KB on all variants.
    Embedded(EmbeddedLogo),
}

#[cw_serde]
pub enum EmbeddedLogo {
    /// Store the Logo as an SVG file. The content must conform to the spec at https://en.wikipedia.org/wiki/Scalable_Vector_Graphics (The contract should do some light-weight sanity-check validation)
    Svg(Binary),
    /// Store the Logo as a PNG file. This will likely only support up to 64x64 or so within the 5KB limit.
    Png(Binary),
}

#[cw_serde]
pub enum ExecuteMsg {
    /// Transfer tokens to another address
    Transfer {
        recipient: String,
        amount: Uint128,
    },
    /// Burn tokens from the sender's balance
    Burn { amount: Uint128 },
    /// Send tokens to a contract and trigger its receive hook
    Send {
        contract: String,
        amount: Uint128,
        msg: Binary,
    },
    /// Set allowance for spender
    IncreaseAllowance {
        spender: String,
        amount: Uint128,
        expires: Option<Expiration>,
    },
    /// Decrease allowance for spender
    DecreaseAllowance {
        spender: String,
        amount: Uint128,
        expires: Option<Expiration>,
    },
    /// Transfer tokens from owner to recipient using allowance
    TransferFrom {
        owner: String,
        recipient: String,
        amount: Uint128,
    },
    /// Send tokens from owner to contract using allowance
    SendFrom {
        owner: String,
        contract: String,
        amount: Uint128,
        msg: Binary,
    },
    
    /// Burn tokens from account using allowance
    BurnFrom { owner: String, amount: Uint128 },
    /// Only with "mintable" extension. Mint new tokens
    Mint { recipient: String, amount: Uint128 },
    /// Special bridge withdraw function that burns tokens and triggers bridge withdrawal
    Withdraw { 
        amount: Uint128,
        destination_address: String, // Ethereum address to receive tokens
    },
    UpdateMetadata {
        name: String,
        symbol: String,
        decimals: u8,
    },
    /// Update marketing metadata
    UpdateMarketing {
        project: Option<String>,
        description: Option<String>,
        marketing: Option<String>,
    },
    /// Upload a logo for the token
    UploadLogo(Logo),
}

#[cw_serde]
pub enum Expiration {
    /// AtHeight will expire when `env.block.height` >= height
    AtHeight(u64),
    /// AtTime will expire when `env.block.time` >= time
    AtTime(cosmwasm_std::Timestamp),
    /// Never will never expire. Used to express the empty variant
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
    /// Returns the current balance of the given address, 0 if unset.
    #[returns(BalanceResponse)]
    Balance { address: String },
    /// Returns metadata on the contract - name, symbol, decimals, etc.
    #[returns(TokenInfoResponse)]
    TokenInfo {},
    /// Returns bridge information - chain ID and original contract address
    #[returns(BridgeInfoResponse)]
    BridgeInfo {},
    /// Returns how much spender can use from owner account, 0 if unset.
    #[returns(AllowanceResponse)]
    Allowance { owner: String, spender: String },
    /// Returns all allowances this owner has approved. Supports pagination.
    #[returns(AllAllowancesResponse)]
    AllAllowances {
        owner: String,
        start_after: Option<String>,
        limit: Option<u32>,
    },
    /// Returns all accounts that have balances. Supports pagination.
    #[returns(AllAccountsResponse)]
    AllAccounts {
        start_after: Option<String>,
        limit: Option<u32>,
    },
    /// Returns metadata for the token (name, symbol, decimals, etc.)
    #[returns(MarketingInfoResponse)]
    MarketingInfo {},
    /// Returns the embedded logo as (style, data), or empty if not set
    #[returns(DownloadLogoResponse)]
    DownloadLogo {},
    /// Only with "mintable" extension. Returns who can mint and the hard cap on total tokens after minting.
    #[returns(MinterResponse)]
    Minter {},

    /// Test gRPC call to fetch approved tokens for trade; returns JSON-normalized data
    #[returns(ApprovedTokensForTradeJson)]
    TestApprovedTokens {},
}

#[cw_serde]
pub struct BalanceResponse {
    pub balance: Uint128,
}

#[cw_serde]
pub struct TokenInfoResponse {
    pub name: String,
    pub symbol: String,
    pub decimals: u8,
    pub total_supply: Uint128,
}

#[cw_serde]
pub struct BridgeInfoResponse {
    pub chain_id: String,
    pub contract_address: String,
}

#[cw_serde]
pub struct AllowanceResponse {
    pub allowance: Uint128,
    pub expires: Expiration,
}

#[cw_serde]
pub struct AllowanceInfo {
    pub spender: String,
    pub allowance: Uint128,
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
    /// A reference to an externally hosted logo. Must be a valid HTTP or HTTPS URL.
    Url(String),
    /// There is an embedded logo on the chain, make another call to DownloadLogo to get it.
    Embedded,
}

#[cw_serde]
pub struct DownloadLogoResponse {
    pub mime_type: String,
    pub data: Binary,
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

#[cw_serde]
pub struct Cw20ReceiveMsg {
    pub sender: String,
    pub amount: Uint128,
    pub msg: Binary,
}