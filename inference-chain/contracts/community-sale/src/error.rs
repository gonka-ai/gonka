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

    #[error("Invalid token: {token}")]
    InvalidToken { token: String },

    #[error("Zero amount not allowed")]
    ZeroAmount {},

    #[error("Insufficient contract balance: {available}, needed: {needed}")]
    InsufficientBalance { available: u128, needed: u128 },

    #[error("Token not accepted: {token}")]
    TokenNotAccepted { token: String },

    #[error("Buyer not allowed: {buyer}")]
    BuyerNotAllowed { buyer: String },

    #[error("Wrong token: expected {expected_chain}:{expected_contract}, got {got_chain}:{got_contract}")]
    WrongToken {
        expected_chain: String,
        expected_contract: String,
        got_chain: String,
        got_contract: String,
    },
}
