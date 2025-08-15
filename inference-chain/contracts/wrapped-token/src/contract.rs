use cosmwasm_std::{
    entry_point, to_json_binary, to_json_vec, Addr, Binary, Deps, DepsMut, Env, MessageInfo, Response,
    StdResult, QueryRequest, GrpcQuery, StdError, ContractResult, SystemResult, Uint128,
};
use cw20_base::contract as cw20_base_contract;
use cw20_base::msg as cw20_base_msg;
use cw_utils::Expiration as CwExpiration;
use cw20::{EmbeddedLogo as CwEmbeddedLogo, Logo as CwLogo};
use cw2::{get_contract_version, set_contract_version};
use cw_storage_plus::Item;
use prost::Message as ProstMessage;

use crate::error::ContractError;
use crate::msg::{
    BridgeInfoResponse, ExecuteMsg, InstantiateMsg, QueryMsg,
    ApprovedTokensForTradeJson, ApprovedTokenJson,
};
use crate::state::{ BridgeInfo, BRIDGE_INFO, TOKEN_METADATA, TokenMetadataOverride };

// Admin storage: stores the address of the contract admin (instantiator)
pub const ADMIN: Item<Addr> = Item::new("admin");

const CONTRACT_NAME: &str = "wrapped-token";
const CONTRACT_VERSION: &str = env!("CARGO_PKG_VERSION");

#[entry_point]
pub fn instantiate(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    msg: InstantiateMsg,
) -> Result<Response, ContractError> {
    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)?;
    // Persist bridge info (extra state)
    BRIDGE_INFO.save(deps.storage, &BridgeInfo { chain_id: msg.chain_id.clone(), contract_address: msg.contract_address.clone() })?;

    // Initialize internal admin to the instantiator address
    ADMIN.save(deps.storage, &info.sender)?;

    // Map our instantiate to cw20-base InstantiateMsg (use placeholders if needed)
    let cw20_init = cw20_base_msg::InstantiateMsg {
        name: "Wrapped Token".to_string(),
        // cw20-base enforces ticker format [a-zA-Z\-]{3,12}
        symbol: "WTKN".to_string(),
        decimals: 6,
        initial_balances: msg.initial_balances.into_iter().map(|c| cw20::Cw20Coin { address: c.address, amount: c.amount }).collect(),
        mint: msg.mint.map(|m| cw20::MinterResponse { minter: m.minter, cap: m.cap }),
        marketing: None,
    };
    let resp = cw20_base_contract::instantiate(deps, env, info, cw20_init)
        .map_err(|e| ContractError::Std(StdError::generic_err(e.to_string())))?;
    Ok(resp)
}

// (Removed: legacy local cw20 state and queries — delegated to cw20-base)

#[entry_point]
pub fn execute(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    msg: ExecuteMsg,
) -> Result<Response, ContractError> {
    match msg {
        // Custom extras
        ExecuteMsg::Withdraw { amount } => withdraw(deps, env, info, amount),
        ExecuteMsg::UpdateMarketing { project, description, marketing } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::UpdateMarketing { project, description, marketing }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::UploadLogo(logo) => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::UploadLogo(map_logo(logo))).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::UpdateMetadata { name, symbol, decimals } => update_metadata(deps, info, name, symbol, decimals),
        // Delegate all standard cw20 ops
        ExecuteMsg::Transfer { recipient, amount } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::Transfer { recipient, amount }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::Burn { amount } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::Burn { amount }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::Send { contract, amount, msg } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::Send { contract, amount, msg }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::Mint { recipient, amount } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::Mint { recipient, amount }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::IncreaseAllowance { spender, amount, expires } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::IncreaseAllowance { spender, amount, expires: map_expiration(expires) }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::DecreaseAllowance { spender, amount, expires } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::DecreaseAllowance { spender, amount, expires: map_expiration(expires) }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::TransferFrom { owner, recipient, amount } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::TransferFrom { owner, recipient, amount }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::SendFrom { owner, contract, amount, msg } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::SendFrom { owner, contract, amount, msg }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
        ExecuteMsg::BurnFrom { owner, amount } => cw20_base_contract::execute(deps, env, info, cw20_base_msg::ExecuteMsg::BurnFrom { owner, amount }).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string()))),
    }
}

fn map_logo(logo: crate::msg::Logo) -> CwLogo {
    match logo {
        crate::msg::Logo::Url(u) => CwLogo::Url(u),
        crate::msg::Logo::Embedded(embed) => match embed {
            crate::msg::EmbeddedLogo::Svg(b) => CwLogo::Embedded(CwEmbeddedLogo::Svg(b)),
            crate::msg::EmbeddedLogo::Png(b) => CwLogo::Embedded(CwEmbeddedLogo::Png(b)),
        },
    }
}

fn map_expiration(exp: Option<crate::msg::Expiration>) -> Option<CwExpiration> {
    exp.map(|e| match e {
        crate::msg::Expiration::AtHeight(h) => CwExpiration::AtHeight(h),
        crate::msg::Expiration::AtTime(t) => CwExpiration::AtTime(t),
        crate::msg::Expiration::Never {} => CwExpiration::Never {},
    })
}

/// Allows the admin to set the token metadata (name, symbol, decimals) after instantiation.
fn update_metadata(
    deps: DepsMut,
    info: MessageInfo,
    name: String,
    symbol: String,
    decimals: u8,
) -> Result<Response, ContractError> {
    // Only admin may call
    let admin = ADMIN.load(deps.storage)?;
    if info.sender != admin {
        return Err(ContractError::Unauthorized {});
    }


    TOKEN_METADATA.save(
        deps.storage,
        &TokenMetadataOverride { name: name.clone(), symbol: symbol.clone(), decimals },
    )?;

    Ok(Response::new()
        .add_attribute("method", "update_metadata")
        .add_attribute("name", name)
        .add_attribute("symbol", symbol)
        .add_attribute("decimals", decimals.to_string()))
}

// Special bridge withdraw function
fn withdraw(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    amount: Uint128,
) -> Result<Response, ContractError> {
    if amount.is_zero() {
        return Err(ContractError::InsufficientFunds {
            balance: 0,
            required: 1,
        });
    }

    // Delegate to cw20-base burn
    let mut resp = cw20_base_contract::execute(
        deps,
        env,
        info,
        cw20_base_msg::ExecuteMsg::Burn { amount },
    ).map_err(|e| ContractError::Std(StdError::generic_err(e.to_string())))?;

    resp = resp
        .add_attribute("method", "withdraw")
        .add_attribute("burn_amount", amount);

    Ok(resp)
}

#[entry_point]
pub fn query(deps: Deps, env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::BridgeInfo {} => to_json_binary(&query_bridge_info(deps)?),
        QueryMsg::Balance { address } => cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::Balance { address }),
        QueryMsg::TokenInfo {} => {
            let base_bin = cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::TokenInfo {})?;
            let mut base: cw20::TokenInfoResponse = cosmwasm_std::from_json(base_bin.clone())?;
            if let Some(override_md) = TOKEN_METADATA.may_load(deps.storage)? {
                base.name = override_md.name;
                base.symbol = override_md.symbol;
                base.decimals = override_md.decimals;
            }
            let resp = crate::msg::TokenInfoResponse {
                name: base.name,
                symbol: base.symbol,
                decimals: base.decimals,
                total_supply: base.total_supply,
            };
            to_json_binary(&resp)
        },
        QueryMsg::Allowance { owner, spender } => cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::Allowance { owner, spender }),
        QueryMsg::AllAllowances { owner, start_after, limit } => cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::AllAllowances { owner, start_after, limit }),
        QueryMsg::AllAccounts { start_after, limit } => cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::AllAccounts { start_after, limit }),
        QueryMsg::MarketingInfo {} => cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::MarketingInfo {}),
        QueryMsg::DownloadLogo {} => cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::DownloadLogo {}),
        QueryMsg::Minter {} => cw20_base_contract::query(deps, env, cw20_base_msg::QueryMsg::Minter {}),
        QueryMsg::TestApprovedTokens {} => to_json_binary(&query_test_approved_tokens(deps)?),
    }
}

#[entry_point]
pub fn migrate(
    deps: DepsMut,
    _env: Env,
    _msg: Binary,
) -> Result<Response, ContractError> {
    let old = get_contract_version(deps.storage)
        .map_err(|e| ContractError::Std(StdError::generic_err(e.to_string())))?;
    if old.contract != CONTRACT_NAME {
        return Err(ContractError::Std(StdError::generic_err(format!(
            "wrong contract: expected {} got {}",
            CONTRACT_NAME, old.contract
        ))));
    }

    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)
        .map_err(|e| ContractError::Std(StdError::generic_err(e.to_string())))?;

    Ok(Response::new()
        .add_attribute("action", "migrate")
        .add_attribute("from_version", old.version)
        .add_attribute("to_version", CONTRACT_VERSION))
}

// Generic helpers for gRPC queries using raw_query serialization pattern
fn query_grpc(deps: Deps, path: &str, data: Binary) -> StdResult<Binary> {
    let request = QueryRequest::Grpc(GrpcQuery {
        path: path.to_string(),
        data,
    });
    query_raw(deps, &request)
}

fn query_raw(deps: Deps, request: &QueryRequest<GrpcQuery>) -> StdResult<Binary> {
    let raw = to_json_vec(request)
        .map_err(|e| StdError::generic_err(format!("Serializing QueryRequest: {e}")))?;
    match deps.querier.raw_query(&raw) {
        SystemResult::Err(system_err) => Err(StdError::generic_err(format!(
            "Querier system error: {system_err}"
        ))),
        SystemResult::Ok(ContractResult::Err(contract_err)) => Err(StdError::generic_err(
            format!("Querier contract error: {contract_err}")
        )),
        SystemResult::Ok(ContractResult::Ok(value)) => Ok(value),
    }
}

fn query_bridge_info(deps: Deps) -> StdResult<BridgeInfoResponse> {
    let info = BRIDGE_INFO.load(deps.storage)?;
    Ok(BridgeInfoResponse {
        chain_id: info.chain_id,
        contract_address: info.contract_address,
    })
}

fn query_test_approved_tokens(deps: Deps) -> StdResult<ApprovedTokensForTradeJson> {
    let decoded: QueryApprovedTokensForTradeResponseProto = query_proto(
        deps,
        "/inference.inference.Query/ApprovedTokensForTrade",
        &EmptyRequest {},
    )?;
    let approved_tokens = decoded
        .approved_tokens
        .into_iter()
        .map(|t| ApprovedTokenJson { chain_id: t.chain_id, contract_address: t.contract_address })
        .collect();
    Ok(ApprovedTokensForTradeJson { approved_tokens })
}

// Proto message types for ApprovedTokensForTrade response
#[derive(Clone, PartialEq, ProstMessage)]
pub struct BridgeTradeApprovedToken {
    #[prost(string, tag = "1")]
    pub chain_id: String,
    #[prost(string, tag = "2")]
    pub contract_address: String,
}

#[derive(Clone, PartialEq, ProstMessage)]
pub struct QueryApprovedTokensForTradeResponseProto {
    #[prost(message, repeated, tag = "1")]
    pub approved_tokens: ::prost::alloc::vec::Vec<BridgeTradeApprovedToken>,
}

#[derive(Clone, PartialEq, ProstMessage)]
pub struct EmptyRequest {}

// Generic helper: encode request proto and decode response proto
fn query_proto<TRequest, TResponse>(deps: Deps, path: &str, request: &TRequest) -> StdResult<TResponse>
where
    TRequest: ProstMessage,
    TResponse: ProstMessage + Default,
{
    let mut buf = Vec::new();
    request
        .encode(&mut buf)
        .map_err(|e| StdError::generic_err(format!("Encode request: {}", e)))?;
    let bytes = query_grpc(deps, path, Binary::from(buf))?;
    TResponse::decode(bytes.as_slice())
        .map_err(|e| StdError::generic_err(format!("Decode response: {}", e)))
}