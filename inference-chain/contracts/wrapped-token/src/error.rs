use cosmwasm_std::{OverflowError, StdError};
use thiserror::Error;

#[derive(Error, Debug, PartialEq)]
pub enum ContractError {
    #[error("{0}")]
    Std(#[from] StdError),

    #[error("{0}")]
    Overflow(#[from] OverflowError),

    #[error("Unauthorized")]
    Unauthorized {},

    #[error("Cannot set to own account")]
    CannotSetOwnAccount {},

    #[error("Cannot set approval that is already expired")]
    Expired {},

    #[error("No allowance for this account")]
    NoAllowance {},

    #[error("Minting cannot exceed the cap")]
    CannotExceedCap {},

    #[error("Duplicate initial balance addresses")]
    DuplicateInitialBalanceAddresses {},

    #[error("Logo binary data exceeds 5KB limit")]
    LogoTooBig {},

    #[error("Invalid XML preamble")]
    InvalidXmlPreamble {},

    #[error("Invalid PNG header")]
    InvalidPngHeader {},

    #[error("Invalid expiration value")]
    InvalidExpiration {},

    #[error("Insufficient funds: balance {balance}, required {required}")]
    InsufficientFunds { balance: String, required: String },

    #[error("Bridge withdrawal not supported yet - query endpoint not ready")]
    WithdrawNotSupported {},

    #[error("Only the module can mint tokens")]
    OnlyModuleCanMint {},

    #[error("Only the module or authorized accounts can burn tokens")]
    OnlyAuthorizedCanBurn {},
}
