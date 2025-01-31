//! A minimal CosmWasm contract demonstrating a counter with all code in one file.
//! Adjust versions in Cargo.toml so that `cw-storage-plus` matches `cosmwasm-std` 1.x

#![allow(deprecated)] // (Optional) Silence "to_binary" deprecated warnings

use cosmwasm_std::{
    entry_point, to_binary, Binary, Deps, DepsMut, Empty, Env, MessageInfo, Response, StdError,
    StdResult,
};
use cw2::set_contract_version;
use cw_storage_plus::Item;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};

// --------------------------------------------------------------------
// Errors
// --------------------------------------------------------------------
#[derive(thiserror::Error, Debug)]
pub enum ContractError {
    #[error("{0}")]
    Std(#[from] StdError),

    #[error("Unauthorized")]
    Unauthorized {},
}

// --------------------------------------------------------------------
// Messages
// --------------------------------------------------------------------

// Initialize the contract
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, JsonSchema)]
pub struct InstantiateMsg {
    pub count: i32,
}

// Execute messages
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, JsonSchema)]
pub enum ExecuteMsg {
    Increment {},
    Reset { count: i32 },
}

// Query messages
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, JsonSchema)]
pub enum QueryMsg {
    GetCount {},
}

// Query response
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, JsonSchema)]
pub struct CountResponse {
    pub count: i32,
}

// --------------------------------------------------------------------
// State
// --------------------------------------------------------------------

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct State {
    pub count: i32,
    pub owner: cosmwasm_std::Addr,
}

// We store the state at key "state"
pub const STATE: Item<State> = Item::new("state");

// --------------------------------------------------------------------
// Constants for contract version
// --------------------------------------------------------------------
const CONTRACT_NAME: &str = "my-contract";
const CONTRACT_VERSION: &str = "0.1.0";

// --------------------------------------------------------------------
// Instantiate
// --------------------------------------------------------------------

#[entry_point]
pub fn instantiate(
    deps: DepsMut<Empty>,
    _env: Env,
    info: MessageInfo,
    msg: InstantiateMsg,
) -> Result<Response<Empty>, ContractError> {
    // record version info for migrations (cw2)
    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)?;

    // store our initial state
    let state = State {
        count: msg.count,
        owner: info.sender.clone(),
    };
    STATE.save(deps.storage, &state)?;

    Ok(Response::new()
        .add_attribute("method", "instantiate")
        .add_attribute("owner", info.sender)
        .add_attribute("count", msg.count.to_string()))
}

// --------------------------------------------------------------------
// Execute
// --------------------------------------------------------------------

#[entry_point]
pub fn execute(
    deps: DepsMut<Empty>,
    _env: Env,
    info: MessageInfo,
    msg: ExecuteMsg,
) -> Result<Response<Empty>, ContractError> {
    match msg {
        ExecuteMsg::Increment {} => execute_increment(deps),
        ExecuteMsg::Reset { count } => execute_reset(deps, info, count),
    }
}

fn execute_increment(deps: DepsMut<Empty>) -> Result<Response<Empty>, ContractError> {
    STATE.update(deps.storage, |mut state| -> StdResult<_> {
        state.count += 1;
        Ok(state)
    })?;

    Ok(Response::new().add_attribute("action", "increment"))
}

fn execute_reset(
    deps: DepsMut<Empty>,
    info: MessageInfo,
    count: i32,
) -> Result<Response<Empty>, ContractError> {
    STATE.update(deps.storage, |mut state| -> Result<_, ContractError> {
        if info.sender != state.owner {
            return Err(ContractError::Unauthorized {});
        }
        state.count = count;
        Ok(state)
    })?;

    Ok(Response::new().add_attribute("action", "reset"))
}

// --------------------------------------------------------------------
// Query
// --------------------------------------------------------------------

#[entry_point]
pub fn query(
    deps: Deps<Empty>,
    _env: Env,
    msg: QueryMsg,
) -> StdResult<Binary> {
    match msg {
        QueryMsg::GetCount {} => to_binary(&query_count(deps)?),
    }
}

fn query_count(deps: Deps<Empty>) -> StdResult<CountResponse> {
    let state = STATE.load(deps.storage)?;
    Ok(CountResponse { count: state.count })
}