use cosmwasm_std::StdError;
use thiserror::Error;

#[derive(Error, Debug)]
pub enum ContractError {
    #[error("{0}")]
    Std(#[from] StdError),

    #[error("Unauthorized")]
    Unauthorized {},

    #[error("Contract is paused")]
    ContractPaused {},

    #[error("Daily limit exceeded. Available: {available}, Requested: {requested}")]
    DailyLimitExceeded {
        available: String,
        requested: String,
    },

    #[error("Invalid token: {token}")]
    InvalidToken { token: String },

    #[error("Invalid exchange rate for token: {token}")]
    InvalidExchangeRate { token: String },

    #[error("Zero amount not allowed")]
    ZeroAmount {},

    #[error("Insufficient contract balance: {available}, needed: {needed}")]
    InsufficientBalance { available: String, needed: String },

    #[error("Invalid basis points: {value}. Must be between 0 and 10000")]
    InvalidBasisPoints { value: cosmwasm_std::Uint256 },

    #[error("Token not accepted: {token}")]
    TokenNotAccepted { token: String },

    #[error("No tokens to purchase")]
    NoTokensToPurchase {},

    #[error("Funds missing: expected exactly one coin in funds")]
    FundsMissing {},
}
