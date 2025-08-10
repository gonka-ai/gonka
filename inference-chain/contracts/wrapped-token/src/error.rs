use cosmwasm_std::StdError;
use thiserror::Error;

#[derive(Error, Debug)]
pub enum ContractError {
    #[error("{0}")]
    Std(#[from] StdError),

    #[error("Unauthorized")]
    Unauthorized {},

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

    #[error("Insufficient funds: balance {balance}, required {required}")]
    InsufficientFunds { balance: u128, required: u128 },

    #[error("Bridge withdrawal not supported yet - query endpoint not ready")]
    WithdrawNotSupported {},

    #[error("Only the module can mint tokens")]
    OnlyModuleCanMint {},

    #[error("Only the module or authorized accounts can burn tokens")]
    OnlyAuthorizedCanBurn {},
}