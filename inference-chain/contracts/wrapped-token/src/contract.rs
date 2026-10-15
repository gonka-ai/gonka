use cosmwasm_std::{
    entry_point, to_json_binary, to_json_vec, Binary, ContractResult, CosmosMsg, Deps, DepsMut,
    Empty, Env, GrpcQuery, MessageInfo, QueryRequest, Response, StdError, StdResult, SystemResult,
    Uint256,
};
use cw2::{get_contract_version, set_contract_version};
use cw_utils::Expiration as CwExpiration;
use prost::Message as ProstMessage;

use crate::error::ContractError;
use crate::msg::{
    ApprovedTokenJson, ApprovedTokensForTradeJson, BridgeInfoResponse, ExecuteMsg, InstantiateMsg,
    QueryMsg,
};
use crate::state::{BridgeInfo, TokenMetadataOverride, BRIDGE_INFO, CREATOR, TOKEN_METADATA};
// `base` is vendored from CosmWasm cw20-base (Uint256 amounts; same storage keys).
use crate::base;
use crate::base::logo::{EmbeddedLogo as CwEmbeddedLogo, Logo as CwLogo};
use crate::base::msg as base_msg;
use crate::base::types::{Cw20Coin, MinterResponse};

const CONTRACT_NAME: &str = "wrapped-token";
const CONTRACT_VERSION: &str = env!("CARGO_PKG_VERSION");
const LEGACY_CW20_BASE_CONTRACT_NAME: &str = "crates.io:cw20-base";

#[entry_point]
pub fn instantiate(
    mut deps: DepsMut,
    env: Env,
    info: MessageInfo,
    msg: InstantiateMsg,
) -> Result<Response, ContractError> {
    // Save creator (instantiator = inference module) - controls operations
    CREATOR.save(deps.storage, &info.sender)?;

    // Save admin (WASM admin = governance module) - controls marketing and metadata
    // Use admin from message if provided, otherwise try to query contract info,
    // falling back to sender if query fails (contract not registered yet during instantiation)
    let admin_addr = if let Some(admin_str) = &msg.admin {
        deps.api.addr_validate(admin_str)?
    } else {
        match deps.querier.query_wasm_contract_info(&env.contract.address) {
            Ok(contract_info) => contract_info.admin.unwrap_or(info.sender.clone()),
            Err(_) => {
                // During instantiation, the contract may not be queryable yet
                // Fall back to sender - the actual admin will be set by the chain
                info.sender.clone()
            }
        }
    };

    // Persist bridge info (extra state)
    BRIDGE_INFO.save(
        deps.storage,
        &BridgeInfo {
            chain_id: msg.chain_id.clone(),
            contract_address: msg.contract_address.clone(),
        },
    )?;

    // Map wrapper instantiate into base InstantiateMsg (placeholders for name/symbol;
    // display metadata can be overridden later via UpdateMetadata).
    let base_init = base_msg::InstantiateMsg {
        name: "Wrapped Token".to_string(),
        // Base enforces ticker format [a-zA-Z\-]{3,12}
        symbol: "WTKN".to_string(),
        decimals: 6,
        initial_balances: msg
            .initial_balances
            .into_iter()
            .map(|c| Cw20Coin {
                address: c.address,
                amount: c.amount,
            })
            .collect(),
        mint: msg.mint.map(|m| MinterResponse {
            minter: m.minter,
            cap: m.cap,
        }),
        // Set marketing account to admin (governance module)
        // This enables UpdateMarketing and UploadLogo functions for governance
        marketing: Some(base_msg::InstantiateMarketingInfo {
            project: Some("Gonka Wrapped Token".to_string()),
            description: Some("Bridge-wrapped token for cross-chain transfers".to_string()),
            marketing: Some(admin_addr.to_string()), // governance module controls marketing
            logo: None,
        }),
    };
    let resp = base::instantiate(deps.branch(), env, info, base_init)?;

    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)
        .map_err(|e| ContractError::Std(StdError::generic_err(e.to_string())))?;

    Ok(resp)
}

#[entry_point]
pub fn execute(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    msg: ExecuteMsg,
) -> Result<Response, ContractError> {
    match msg {
        // Custom extras
        ExecuteMsg::Withdraw {
            amount,
            destination_address,
            destination_bridge_address,
        } => withdraw(
            deps,
            env,
            info,
            amount,
            destination_address,
            destination_bridge_address,
        ),
        ExecuteMsg::UpdateMetadata {
            name,
            symbol,
            decimals,
        } => update_metadata(deps, env, info, name, symbol, decimals),
        // Delegate standard CW20-shaped ops to base
        ExecuteMsg::Transfer { recipient, amount } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::Transfer { recipient, amount },
        ),
        ExecuteMsg::Burn { amount } => {
            base::execute(deps, env, info, base_msg::ExecuteMsg::Burn { amount })
        }
        ExecuteMsg::Send {
            contract,
            amount,
            msg,
        } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::Send {
                contract,
                amount,
                msg,
            },
        ),
        ExecuteMsg::Mint { recipient, amount } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::Mint { recipient, amount },
        ),
        ExecuteMsg::IncreaseAllowance {
            spender,
            amount,
            expires,
        } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::IncreaseAllowance {
                spender,
                amount,
                expires: map_expiration(expires),
            },
        ),
        ExecuteMsg::DecreaseAllowance {
            spender,
            amount,
            expires,
        } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::DecreaseAllowance {
                spender,
                amount,
                expires: map_expiration(expires),
            },
        ),
        ExecuteMsg::TransferFrom {
            owner,
            recipient,
            amount,
        } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::TransferFrom {
                owner,
                recipient,
                amount,
            },
        ),
        ExecuteMsg::SendFrom {
            owner,
            contract,
            amount,
            msg,
        } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::SendFrom {
                owner,
                contract,
                amount,
                msg,
            },
        ),
        ExecuteMsg::BurnFrom { owner, amount } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::BurnFrom { owner, amount },
        ),
        ExecuteMsg::UpdateMarketing {
            project,
            description,
            marketing,
        } => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::UpdateMarketing {
                project,
                description,
                marketing,
            },
        ),
        ExecuteMsg::UploadLogo(logo) => base::execute(
            deps,
            env,
            info,
            base_msg::ExecuteMsg::UploadLogo(map_logo(logo)),
        ),
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

/// Validates a 42-character `0x`-prefixed hex string (matches chain `isValidEthereumAddress`).
fn validate_ethereum_address(field: &str, raw: &str) -> Result<String, ContractError> {
    let t = raw.trim();
    if t.is_empty() {
        return Err(ContractError::Std(StdError::generic_err(format!(
            "{field} cannot be empty"
        ))));
    }
    if t.len() != 42 || !(t.starts_with("0x") || t.starts_with("0X")) {
        return Err(ContractError::Std(StdError::generic_err(format!(
            "{field} must be a 42-character hex address starting with 0x"
        ))));
    }
    if !t[2..].chars().all(|c| c.is_ascii_hexdigit()) {
        return Err(ContractError::Std(StdError::generic_err(format!(
            "{field} contains invalid characters"
        ))));
    }
    Ok(t.to_string())
}

/// Allows both creator (inference module) and admin (governance module) to update token metadata.
fn update_metadata(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    name: String,
    symbol: String,
    decimals: u8,
) -> Result<Response, ContractError> {
    // Load creator (inference module)
    let creator = CREATOR.load(deps.storage)?;

    // Dynamically query admin (governance module)
    let is_admin = deps
        .querier
        .query_wasm_contract_info(&env.contract.address)
        .map(|contract_info| {
            contract_info
                .admin
                .is_some_and(|admin| admin == info.sender)
        })
        .unwrap_or(false);

    // Allow both creator (inference module) and admin (governance module) to update metadata
    let is_creator = info.sender == creator;

    if !is_creator && !is_admin {
        return Err(ContractError::Unauthorized {});
    }

    TOKEN_METADATA.save(
        deps.storage,
        &TokenMetadataOverride {
            name: name.clone(),
            symbol: symbol.clone(),
            decimals,
        },
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
    amount: Uint256,
    destination_address: String,
    destination_bridge_address: String,
) -> Result<Response, ContractError> {
    if amount.is_zero() {
        return Err(ContractError::InsufficientFunds {
            balance: "0".to_string(),
            required: "1".to_string(),
        });
    }

    let destination_address =
        validate_ethereum_address("destination_address", &destination_address)?;
    let destination_bridge_address =
        validate_ethereum_address("destination_bridge_address", &destination_bridge_address)?;

    // Delegate burn to base
    let mut resp = base::execute(
        deps,
        env.clone(),
        info.clone(),
        base_msg::ExecuteMsg::Burn { amount },
    )?;

    // Create the bridge withdrawal message
    let bridge_msg = create_bridge_withdrawal_msg(
        env.contract.address.to_string(), // creator (this contract - will be the transaction signer)
        info.sender.to_string(),          // user_address (the caller)
        amount.to_string(),               // amount
        destination_address.clone(),
        destination_bridge_address.clone(),
    )?;

    resp = resp
        .add_message(bridge_msg)
        .add_attribute("method", "withdraw")
        .add_attribute("burn_amount", amount)
        .add_attribute("destination_address", destination_address)
        .add_attribute("destination_bridge_address", destination_bridge_address);

    Ok(resp)
}

// Proto message for MsgRequestBridgeWithdrawal
#[derive(Clone, PartialEq, ProstMessage)]
pub struct MsgRequestBridgeWithdrawal {
    #[prost(string, tag = "1")]
    pub creator: String,
    #[prost(string, tag = "2")]
    pub user_address: String,
    #[prost(string, tag = "3")]
    pub amount: String,
    #[prost(string, tag = "4")]
    pub destination_address: String,
    #[prost(string, tag = "5")]
    pub destination_bridge_address: String,
}

// Helper function to create the bridge withdrawal message
fn create_bridge_withdrawal_msg(
    creator: String,
    user_address: String,
    amount: String,
    destination_address: String,
    destination_bridge_address: String,
) -> Result<CosmosMsg, ContractError> {
    // Create the protobuf message
    let msg = MsgRequestBridgeWithdrawal {
        creator,
        user_address,
        amount,
        destination_address,
        destination_bridge_address,
    };

    // Encode the message as protobuf
    let mut buf = Vec::new();
    msg.encode(&mut buf).map_err(|e| {
        ContractError::Std(StdError::generic_err(format!(
            "Failed to encode withdrawal message: {e}"
        )))
    })?;

    let stargate_msg = CosmosMsg::Any(cosmwasm_std::AnyMsg {
        type_url: "/inference.inference.MsgRequestBridgeWithdrawal".to_string(),
        value: Binary::from(buf),
    });

    Ok(stargate_msg)
}

#[entry_point]
pub fn query(deps: Deps, env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::BridgeInfo {} => to_json_binary(&query_bridge_info(deps)?),
        QueryMsg::Balance { address } => {
            base::query(deps, env, base_msg::QueryMsg::Balance { address })
        }
        QueryMsg::TokenInfo {} => {
            let base_bin = base::query(deps, env, base_msg::QueryMsg::TokenInfo {})?;
            let mut base: base::TokenInfoResponse = cosmwasm_std::from_json(base_bin.clone())?;
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
        }
        QueryMsg::Allowance { owner, spender } => {
            base::query(deps, env, base_msg::QueryMsg::Allowance { owner, spender })
        }
        QueryMsg::AllAllowances {
            owner,
            start_after,
            limit,
        } => base::query(
            deps,
            env,
            base_msg::QueryMsg::AllAllowances {
                owner,
                start_after,
                limit,
            },
        ),
        QueryMsg::AllAccounts { start_after, limit } => base::query(
            deps,
            env,
            base_msg::QueryMsg::AllAccounts { start_after, limit },
        ),
        QueryMsg::MarketingInfo {} => base::query(deps, env, base_msg::QueryMsg::MarketingInfo {}),
        QueryMsg::DownloadLogo {} => base::query(deps, env, base_msg::QueryMsg::DownloadLogo {}),
        QueryMsg::Minter {} => base::query(deps, env, base_msg::QueryMsg::Minter {}),
        QueryMsg::TestApprovedTokens {} => to_json_binary(&query_test_approved_tokens(deps)?),
    }
}

#[entry_point]
pub fn migrate(deps: DepsMut, _env: Env, _msg: Empty) -> Result<Response, ContractError> {
    let old = get_contract_version(deps.storage)
        .map_err(|e| ContractError::Std(StdError::generic_err(e.to_string())))?;

    let is_legacy_cw20_base = old.contract == LEGACY_CW20_BASE_CONTRACT_NAME;
    if old.contract != CONTRACT_NAME && !is_legacy_cw20_base {
        return Err(ContractError::Std(StdError::generic_err(format!(
            "wrong contract: expected {} or {}, got {}",
            CONTRACT_NAME, LEGACY_CW20_BASE_CONTRACT_NAME, old.contract
        ))));
    }

    if is_legacy_cw20_base {
        CREATOR
            .may_load(deps.storage)?
            .ok_or_else(|| StdError::generic_err("missing wrapped-token legacy state: creator"))?;
        BRIDGE_INFO.may_load(deps.storage)?.ok_or_else(|| {
            StdError::generic_err("missing wrapped-token legacy state: bridge_info")
        })?;
    }

    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)
        .map_err(|e| ContractError::Std(StdError::generic_err(e.to_string())))?;

    Ok(Response::new()
        .add_attribute("action", "migrate")
        .add_attribute("from_contract", old.contract)
        .add_attribute("from_version", old.version)
        .add_attribute("to_version", CONTRACT_VERSION)
        .add_attribute("amount_domain", "uint256"))
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
        SystemResult::Ok(ContractResult::Err(contract_err)) => Err(StdError::generic_err(format!(
            "Querier contract error: {contract_err}"
        ))),
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
        .map(|t| ApprovedTokenJson {
            chain_id: t.chain_id,
            contract_address: t.contract_address,
        })
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
fn query_proto<TRequest, TResponse>(
    deps: Deps,
    path: &str,
    request: &TRequest,
) -> StdResult<TResponse>
where
    TRequest: ProstMessage,
    TResponse: ProstMessage + Default,
{
    let mut buf = Vec::new();
    request
        .encode(&mut buf)
        .map_err(|e| StdError::generic_err(format!("Encode request: {e}")))?;
    let bytes = query_grpc(deps, path, Binary::from(buf))?;
    TResponse::decode(bytes.as_slice())
        .map_err(|e| StdError::generic_err(format!("Decode response: {e}")))
}

#[cfg(test)]
mod tests {
    use super::*;
    use cosmwasm_std::testing::{message_info, mock_dependencies, mock_env, MockApi};
    use cosmwasm_std::{from_json, Empty, Uint256};

    fn addr(s: &str) -> cosmwasm_std::Addr {
        MockApi::default().addr_make(s)
    }

    fn instantiate_msg() -> InstantiateMsg {
        InstantiateMsg {
            chain_id: "ethereum-mainnet".to_string(),
            contract_address: "0x1111111111111111111111111111111111111111".to_string(),
            initial_balances: vec![],
            mint: None,
            admin: None,
        }
    }

    fn seed_legacy_wrapped_token_state(deps: DepsMut) {
        let creator = addr("inference-module");
        CREATOR
            .save(deps.storage, &creator)
            .expect("creator should be stored");
        BRIDGE_INFO
            .save(
                deps.storage,
                &BridgeInfo {
                    chain_id: "ethereum-mainnet".to_string(),
                    contract_address: "0x1111111111111111111111111111111111111111".to_string(),
                },
            )
            .expect("bridge info should be stored");
    }

    #[test]
    fn instantiate_sets_wrapped_token_cw2_marker() {
        let mut deps = mock_dependencies();
        let env = mock_env();
        let sender = addr("inference-module");
        let info = message_info(&sender, &[]);

        instantiate(deps.as_mut(), env, info, instantiate_msg())
            .expect("instantiate should succeed");

        let version =
            get_contract_version(&deps.storage).expect("contract version should be stored");
        assert_eq!(version.contract, CONTRACT_NAME);
        assert_eq!(version.version, CONTRACT_VERSION);
    }

    #[test]
    fn migrate_accepts_legacy_cw20_base_marker() {
        let mut deps = mock_dependencies();
        set_contract_version(
            deps.as_mut().storage,
            LEGACY_CW20_BASE_CONTRACT_NAME,
            "2.0.0",
        )
        .expect("legacy marker should be stored");
        seed_legacy_wrapped_token_state(deps.as_mut());

        // Gov path sends migrate_msg_json="{}" — CosmWasm from_json's into Empty before migrate().
        let msg: Empty = from_json(br"{}").expect("{} must deserialize as Empty");
        migrate(deps.as_mut(), mock_env(), msg).expect("migration should succeed");

        let version =
            get_contract_version(&deps.storage).expect("contract version should be updated");
        assert_eq!(version.contract, CONTRACT_NAME);
        assert_eq!(version.version, CONTRACT_VERSION);
    }

    #[test]
    fn migrate_msg_rejects_binary_for_empty_object() {
        // Documents why migrate takes Empty, not Binary: Binary expects a JSON string (base64).
        assert!(from_json::<Binary>(br"{}").is_err());
        assert!(from_json::<Empty>(br"{}").is_ok());
    }

    #[test]
    fn migrate_rejects_foreign_legacy_cw20_base_marker() {
        let mut deps = mock_dependencies();
        set_contract_version(
            deps.as_mut().storage,
            LEGACY_CW20_BASE_CONTRACT_NAME,
            "2.0.0",
        )
        .expect("legacy marker should be stored");

        let err = migrate(deps.as_mut(), mock_env(), Empty {})
            .expect_err("migration should fail without wrapped-token legacy state");
        assert!(
            err.to_string()
                .contains("missing wrapped-token legacy state"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn mint_accepts_amount_above_uint128_max() {
        let mut deps = mock_dependencies();
        let env = mock_env();
        let minter = addr("inference-module");
        let recipient = addr("recipient");
        let info = message_info(&minter, &[]);

        let mut msg = instantiate_msg();
        msg.mint = Some(crate::msg::MinterResponse {
            minter: minter.to_string(),
            cap: None,
        });
        instantiate(deps.as_mut(), env.clone(), info.clone(), msg).unwrap();

        let amount = "340282366920938463463374607431768211456"
            .parse::<Uint256>()
            .unwrap();
        execute(
            deps.as_mut(),
            env.clone(),
            info,
            ExecuteMsg::Mint {
                recipient: recipient.to_string(),
                amount,
            },
        )
        .expect("mint above Uint128::MAX should succeed");

        let bal: crate::msg::BalanceResponse = cosmwasm_std::from_json(
            query(
                deps.as_ref(),
                env,
                QueryMsg::Balance {
                    address: recipient.to_string(),
                },
            )
            .unwrap(),
        )
        .unwrap();
        assert_eq!(bal.balance, amount);
    }

    #[test]
    fn legacy_uint128_balance_json_loads_as_uint256() {
        let mut deps = mock_dependencies();
        let owner = addr("holder");
        use cw_storage_plus::Map;
        const OLD: Map<&cosmwasm_std::Addr, cosmwasm_std::Uint128> = Map::new("balance");
        OLD.save(
            deps.as_mut().storage,
            &owner,
            &cosmwasm_std::Uint128::new(42),
        )
        .unwrap();

        let loaded = crate::base::state::BALANCES
            .load(deps.as_ref().storage, &owner)
            .unwrap();
        assert_eq!(loaded, Uint256::from(42u128));
    }
}
