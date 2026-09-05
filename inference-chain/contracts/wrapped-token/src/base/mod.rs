//! Token engine based on CosmWasm cw20-base, adapted to Uint256 for Gonka wrapped ERC-20.
//! Storage keys match cw20-base 2.0.0 so existing instances can migrate in place.

pub mod allowances;
pub mod contract;
pub mod enumerable;
pub mod logo;
pub mod msg;
pub mod receiver;
pub mod state;
pub mod types;

pub use contract::{execute, instantiate, query};
pub use msg::{ExecuteMsg, InstantiateMarketingInfo, InstantiateMsg, QueryMsg};
pub use types::*;
