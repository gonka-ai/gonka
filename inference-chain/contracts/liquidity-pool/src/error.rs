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
    DailyLimitExceeded { available: u128, requested: u128 },

    #[error("Invalid token: {token}")]
    InvalidToken { token: String },

    #[error("Invalid exchange rate for token: {token}")]
    InvalidExchangeRate { token: String },

    #[error("Zero amount not allowed")]
    ZeroAmount {},

    #[error("Insufficient contract balance: {available}, needed: {needed}")]
    InsufficientBalance { available: u128, needed: u128 },

    #[error("Invalid basis points: {value}. Must be between 0 and 10000")]
    InvalidBasisPoints { value: cosmwasm_std::Uint128 },

    #[error("Token not accepted: {token}")]
    TokenNotAccepted { token: String },

    #[error("No tokens to purchase")]
    NoTokensToPurchase {},
} 