use cosmwasm_std::{
    entry_point, from_json, to_json_binary, to_json_vec, BankMsg, Binary, Coin, Deps, DepsMut,
    Env, MessageInfo, Response, StdError, StdResult, Uint128, QueryRequest, GrpcQuery,
    ContractResult, SystemResult, WasmMsg, WasmQuery,
};
use prost::Message;
use cw2::{get_contract_version, set_contract_version};

use crate::error::ContractError;
use crate::msg::{
    ConfigResponse, Cw20ReceiveMsg, ExecuteMsg, InstantiateMsg,
    NativeBalanceResponse, PurchaseTokenMsg, QueryMsg, TestBridgeValidationResponse,
    TokenCalculationResponse, BlockHeightResponse, ApprovedTokensForTradeJson, ApprovedTokenJson,
};
use crate::state::{calculate_tokens_for_usd, Config, CONFIG};

#[derive(Clone, PartialEq, Message)]
pub struct QueryValidateWrappedTokenForTradeRequest {
    #[prost(string, tag = "1")]
    pub contract_address: String,
}

#[derive(Clone, PartialEq, Message)]
pub struct QueryValidateWrappedTokenForTradeResponse {
    #[prost(bool, tag = "1")]
    pub is_valid: bool,
}

#[derive(Clone, PartialEq, Message, serde::Serialize)]
pub struct BridgeTradeApprovedToken {
    #[prost(string, tag = "1")]
    pub chain_id: String,
    #[prost(string, tag = "2")]
    pub contract_address: String,
}

#[derive(Clone, PartialEq, Message, serde::Serialize)]
pub struct QueryApprovedTokensForTradeResponseProto {
    #[prost(message, repeated, tag = "1")]
    pub approved_tokens: ::prost::alloc::vec::Vec<BridgeTradeApprovedToken>,
}

#[derive(Clone, PartialEq, Message)]
pub struct EmptyRequest {}

#[derive(Clone, PartialEq, Message)]
pub struct QueryTotalSupplyRequest {}

#[derive(Clone, PartialEq, Message)]
pub struct QueryTotalSupplyResponse {
    #[prost(message, repeated, tag = "1")]
    pub supply: ::prost::alloc::vec::Vec<CoinProto>,
}

#[derive(Clone, PartialEq, Message)]
pub struct CoinProto {
    #[prost(string, tag = "1")]
    pub denom: String,
    #[prost(string, tag = "2")]
    pub amount: String,
}

const CONTRACT_NAME: &str = "community-sale";
const CONTRACT_VERSION: &str = env!("CARGO_PKG_VERSION");

fn validate_wrapped_token_for_trade(deps: Deps, token_identifier: &str) -> Result<bool, ContractError> {
    let contract_address = token_identifier
        .strip_prefix("cw20:")
        .unwrap_or(token_identifier);

    let request = QueryValidateWrappedTokenForTradeRequest {
        contract_address: contract_address.to_string(),
    };
    let response: QueryValidateWrappedTokenForTradeResponse = query_proto(
        deps,
        "/inference.inference.Query/ValidateWrappedTokenForTrade",
        &request,
    )
    .map_err(ContractError::Std)?;

    Ok(response.is_valid)
}

fn get_native_denom(deps: Deps) -> Result<String, ContractError> {
    let request = QueryTotalSupplyRequest {};
    match query_proto::<QueryTotalSupplyRequest, QueryTotalSupplyResponse>(
        deps,
        "/cosmos.bank.v1beta1.Query/TotalSupply",
        &request,
    ) {
        Ok(response) => {
            if let Some(coin) = response.supply.first() {
                if !coin.denom.is_empty() {
                    return Ok(coin.denom.clone());
                }
            }
            Ok("ngonka".to_string())
        }
        Err(_) => Ok("ngonka".to_string()),
    }
}

fn create_cw20_transfer_msg(
    cw20_contract: String,
    recipient: String,
    amount: Uint128,
) -> Result<WasmMsg, ContractError> {
    let transfer_msg_str = format!(
        r#"{{"transfer":{{"recipient":"{}","amount":"{}"}}}}"#,
        recipient, amount
    );
    Ok(WasmMsg::Execute {
        contract_addr: cw20_contract,
        msg: Binary::from(transfer_msg_str.as_bytes()),
        funds: vec![],
    })
}

/// Query message for wrapped token's BridgeInfo
#[derive(serde::Serialize)]
struct BridgeInfoQuery {}

/// Response from wrapped token's BridgeInfo query
#[derive(serde::Deserialize)]
struct BridgeInfoResponse {
    pub chain_id: String,
    pub contract_address: String,
}

/// Query CW20 wrapped token for its underlying bridge info (chain_id, eth_contract)
fn query_bridge_info(deps: Deps, cw20_addr: &str) -> Result<(String, String), ContractError> {
    #[derive(serde::Serialize)]
    struct QueryMsg {
        bridge_info: BridgeInfoQuery,
    }
    
    let query_msg = QueryMsg { bridge_info: BridgeInfoQuery {} };
    let response: BridgeInfoResponse = deps.querier.query(&QueryRequest::Wasm(WasmQuery::Smart {
        contract_addr: cw20_addr.to_string(),
        msg: to_json_binary(&query_msg)
            .map_err(|e| ContractError::Std(StdError::msg(format!("serialize: {}", e))))?,
    })).map_err(|e| ContractError::Std(StdError::msg(format!("query bridge_info: {}", e))))?;
    
    Ok((response.chain_id, response.contract_address.to_lowercase()))
}

#[entry_point]
pub fn instantiate(
    deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    msg: InstantiateMsg,
) -> Result<Response, ContractError> {
    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)
        .map_err(|e| ContractError::Std(StdError::msg(e.to_string())))?;

    let admin = deps.api.addr_validate(&msg.admin)?.to_string();
    let buyer = deps.api.addr_validate(&msg.buyer)?.to_string();

    if msg.price_usd.is_zero() {
        return Err(ContractError::ZeroAmount {});
    }

    if msg.accepted_chain_id.is_empty() || msg.accepted_eth_contract.is_empty() {
        return Err(ContractError::Std(StdError::msg("accepted_chain_id and accepted_eth_contract required")));
    }

    let native_denom = get_native_denom(deps.as_ref())?;

    let config = Config {
        admin: admin.clone(),
        buyer: buyer.clone(),
        accepted_chain_id: msg.accepted_chain_id.clone(),
        accepted_eth_contract: msg.accepted_eth_contract.to_lowercase(),
        price_usd: msg.price_usd,
        native_denom: native_denom.clone(),
        is_paused: false,
        total_tokens_sold: Uint128::zero(),
    };
    CONFIG.save(deps.storage, &config)?;

    Ok(Response::new()
        .add_attribute("method", "instantiate")
        .add_attribute("admin", admin)
        .add_attribute("buyer", buyer)
        .add_attribute("accepted_chain_id", msg.accepted_chain_id)
        .add_attribute("accepted_eth_contract", msg.accepted_eth_contract)
        .add_attribute("price_usd", msg.price_usd)
        .add_attribute("native_denom", native_denom))
}

#[entry_point]
pub fn execute(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    msg: ExecuteMsg,
) -> Result<Response, ContractError> {
    match msg {
        ExecuteMsg::Receive(msg) => receive_cw20(deps, env, info, msg),
        ExecuteMsg::Pause {} => pause_contract(deps, info),
        ExecuteMsg::Resume {} => resume_contract(deps, info),
        ExecuteMsg::UpdateBuyer { buyer } => update_buyer(deps, info, buyer),
        ExecuteMsg::UpdatePrice { price_usd } => update_price(deps, info, price_usd),
        ExecuteMsg::WithdrawNativeTokens { amount, recipient } => withdraw_native_tokens(deps, info, amount, recipient),
        ExecuteMsg::EmergencyWithdraw { recipient } => emergency_withdraw(deps, env, info, recipient),
    }
}

fn receive_cw20(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    cw20_msg: Cw20ReceiveMsg,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;

    if config.is_paused {
        return Err(ContractError::ContractPaused {});
    }

    let cw20_contract = info.sender.to_string();

    // Check 1: Only designated buyer can purchase
    if cw20_msg.sender != config.buyer {
        return Err(ContractError::BuyerNotAllowed {
            buyer: cw20_msg.sender.clone(),
        });
    }

    // Check 2: Validate it's a legit bridge token via chain
    if !validate_wrapped_token_for_trade(deps.as_ref(), &cw20_contract)? {
        return Err(ContractError::TokenNotAccepted {
            token: format!("CW20 {} not approved for trading", cw20_contract),
        });
    }

    // Check 3: Query underlying Ethereum address and compare to expected
    let (chain_id, eth_contract) = query_bridge_info(deps.as_ref(), &cw20_contract)?;
    if chain_id != config.accepted_chain_id || eth_contract != config.accepted_eth_contract {
        return Err(ContractError::WrongToken {
            expected_chain: config.accepted_chain_id.clone(),
            expected_contract: config.accepted_eth_contract.clone(),
            got_chain: chain_id,
            got_contract: eth_contract,
        });
    }

    let _purchase_msg: PurchaseTokenMsg = from_json(&cw20_msg.msg)?;
    let buyer = cw20_msg.sender;
    let usd_amount = cw20_msg.amount;

    if usd_amount.is_zero() {
        return Err(ContractError::ZeroAmount {});
    }

    // Fixed price calculation
    let tokens_to_buy = calculate_tokens_for_usd(usd_amount, config.price_usd);
    if tokens_to_buy.is_zero() {
        return Err(ContractError::ZeroAmount {});
    }

    // Check contract balance
    let contract_balance = deps
        .querier
        .query_balance(env.contract.address.to_string(), &config.native_denom)?;

    let balance_u128: Uint128 = contract_balance
        .amount
        .try_into()
        .map_err(|_| ContractError::Std(StdError::msg("balance exceeds Uint128")))?;

    if tokens_to_buy > balance_u128 {
        return Err(ContractError::InsufficientBalance {
            available: balance_u128.u128(),
            needed: tokens_to_buy.u128(),
        });
    }

    // Update total sold
    let mut updated_config = config.clone();
    updated_config.total_tokens_sold = updated_config
        .total_tokens_sold
        .checked_add(tokens_to_buy)
        .map_err(|e| ContractError::Std(StdError::msg(format!("overflow: {}", e))))?;
    CONFIG.save(deps.storage, &updated_config)?;

    // Send GNK to buyer
    let send_native_msg = BankMsg::Send {
        to_address: buyer.clone(),
        amount: vec![Coin {
            denom: config.native_denom.clone(),
            amount: tokens_to_buy.into(),
        }],
    };

    // Forward W(USDT) to admin
    let mut response = Response::new().add_message(send_native_msg);
    if !config.admin.is_empty() {
        let transfer_cw20_msg = create_cw20_transfer_msg(
            cw20_contract.clone(),
            config.admin.clone(),
            usd_amount,
        )?;
        response = response.add_message(transfer_cw20_msg);
    }

    Ok(response
        .add_attribute("method", "purchase")
        .add_attribute("buyer", buyer)
        .add_attribute("usdt_amount", usd_amount)
        .add_attribute("gnk_purchased", tokens_to_buy)
        .add_attribute("price_usd", config.price_usd))
}

fn pause_contract(deps: DepsMut, info: MessageInfo) -> Result<Response, ContractError> {
    let mut config = CONFIG.load(deps.storage)?;
    if info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }
    config.is_paused = true;
    CONFIG.save(deps.storage, &config)?;
    Ok(Response::new().add_attribute("method", "pause"))
}

fn resume_contract(deps: DepsMut, info: MessageInfo) -> Result<Response, ContractError> {
    let mut config = CONFIG.load(deps.storage)?;
    if info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }
    config.is_paused = false;
    CONFIG.save(deps.storage, &config)?;
    Ok(Response::new().add_attribute("method", "resume"))
}

fn update_buyer(deps: DepsMut, info: MessageInfo, buyer: String) -> Result<Response, ContractError> {
    let mut config = CONFIG.load(deps.storage)?;
    if info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }
    let validated_buyer = deps.api.addr_validate(&buyer)?.to_string();
    config.buyer = validated_buyer.clone();
    CONFIG.save(deps.storage, &config)?;
    Ok(Response::new()
        .add_attribute("method", "update_buyer")
        .add_attribute("buyer", validated_buyer))
}

fn update_price(deps: DepsMut, info: MessageInfo, price_usd: Uint128) -> Result<Response, ContractError> {
    let mut config = CONFIG.load(deps.storage)?;
    if info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }
    if price_usd.is_zero() {
        return Err(ContractError::ZeroAmount {});
    }
    config.price_usd = price_usd;
    CONFIG.save(deps.storage, &config)?;
    Ok(Response::new()
        .add_attribute("method", "update_price")
        .add_attribute("price_usd", price_usd))
}

fn withdraw_native_tokens(
    deps: DepsMut,
    info: MessageInfo,
    amount: Uint128,
    recipient: String,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;
    if info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }
    let recipient_addr = deps.api.addr_validate(&recipient)?;
    if amount.is_zero() {
        return Err(ContractError::ZeroAmount {});
    }
    let send_msg = BankMsg::Send {
        to_address: recipient_addr.to_string(),
        amount: vec![Coin {
            denom: config.native_denom,
            amount: amount.into(),
        }],
    };
    Ok(Response::new()
        .add_message(send_msg)
        .add_attribute("method", "withdraw")
        .add_attribute("amount", amount)
        .add_attribute("recipient", recipient))
}

fn emergency_withdraw(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    recipient: String,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;
    if info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }
    let recipient_addr = deps.api.addr_validate(&recipient)?;
    let balance = deps
        .querier
        .query_balance(env.contract.address.to_string(), &config.native_denom)?;

    if balance.amount.is_zero() {
        return Ok(Response::new()
            .add_attribute("method", "emergency_withdraw")
            .add_attribute("message", "no_funds"));
    }

    let send_msg = BankMsg::Send {
        to_address: recipient_addr.to_string(),
        amount: vec![balance.clone()],
    };
    Ok(Response::new()
        .add_message(send_msg)
        .add_attribute("method", "emergency_withdraw")
        .add_attribute("amount", balance.amount)
        .add_attribute("recipient", recipient))
}

#[entry_point]
pub fn query(deps: Deps, env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::Config {} => to_json_binary(&query_config(deps)?),
        QueryMsg::NativeBalance {} => to_json_binary(&query_native_balance(deps, env)?),
        QueryMsg::CalculateTokens { usd_amount } => to_json_binary(&query_calculate_tokens(deps, usd_amount)?),
        QueryMsg::TestBridgeValidation { cw20_contract } => to_json_binary(&query_test_bridge_validation(deps, cw20_contract)?),
        QueryMsg::BlockHeight {} => to_json_binary(&query_block_height(env)?),
        QueryMsg::TestApprovedTokens {} => to_json_binary(&query_test_approved_tokens(deps)?),
    }
}

#[entry_point]
pub fn migrate(deps: DepsMut, _env: Env, _msg: Binary) -> Result<Response, ContractError> {
    let old = get_contract_version(deps.storage)
        .map_err(|e| ContractError::Std(StdError::msg(e.to_string())))?;
    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)
        .map_err(|e| ContractError::Std(StdError::msg(e.to_string())))?;
    Ok(Response::new()
        .add_attribute("action", "migrate")
        .add_attribute("from_version", old.version)
        .add_attribute("to_version", CONTRACT_VERSION))
}

fn query_config(deps: Deps) -> StdResult<ConfigResponse> {
    let config = CONFIG.load(deps.storage)?;
    Ok(ConfigResponse {
        admin: config.admin,
        buyer: config.buyer,
        accepted_chain_id: config.accepted_chain_id,
        accepted_eth_contract: config.accepted_eth_contract,
        price_usd: config.price_usd,
        native_denom: config.native_denom,
        is_paused: config.is_paused,
        total_tokens_sold: config.total_tokens_sold,
    })
}

fn query_native_balance(deps: Deps, env: Env) -> StdResult<NativeBalanceResponse> {
    let config = CONFIG.load(deps.storage)?;
    let balance = deps
        .querier
        .query_balance(&env.contract.address, &config.native_denom)?;
    Ok(NativeBalanceResponse { balance })
}

fn query_calculate_tokens(deps: Deps, usd_amount: Uint128) -> StdResult<TokenCalculationResponse> {
    let config = CONFIG.load(deps.storage)?;
    let tokens = calculate_tokens_for_usd(usd_amount, config.price_usd);
    Ok(TokenCalculationResponse {
        tokens,
        price_usd: config.price_usd,
    })
}

fn query_test_bridge_validation(deps: Deps, cw20_contract: String) -> StdResult<TestBridgeValidationResponse> {
    let denom = if cw20_contract.starts_with("cw20:") {
        cw20_contract
    } else {
        format!("cw20:{}", cw20_contract)
    };
    let is_valid = validate_wrapped_token_for_trade(deps, &denom).unwrap_or(false);
    Ok(TestBridgeValidationResponse { is_valid })
}

fn query_block_height(env: Env) -> StdResult<BlockHeightResponse> {
    Ok(BlockHeightResponse { height: env.block.height })
}

fn query_test_approved_tokens(deps: Deps) -> StdResult<ApprovedTokensForTradeJson> {
    let decoded: QueryApprovedTokensForTradeResponseProto = query_proto(
        deps,
        "/inference.inference.Query/ApprovedTokensForTrade",
        &EmptyRequest::default(),
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

fn query_grpc(deps: Deps, path: &str, data: Binary) -> StdResult<Binary> {
    let request = QueryRequest::Grpc(GrpcQuery {
        path: path.to_string(),
        data,
    });
    query_raw(deps, &request)
}

fn query_raw(deps: Deps, request: &QueryRequest<GrpcQuery>) -> StdResult<Binary> {
    let raw = to_json_vec(request).map_err(|e| StdError::msg(format!("Serializing: {e}")))?;
    match deps.querier.raw_query(&raw) {
        SystemResult::Err(e) => Err(StdError::msg(format!("System error: {e}"))),
        SystemResult::Ok(ContractResult::Err(e)) => Err(StdError::msg(format!("Contract error: {e}"))),
        SystemResult::Ok(ContractResult::Ok(value)) => Ok(value),
    }
}

fn query_proto<TRequest, TResponse>(deps: Deps, path: &str, request: &TRequest) -> StdResult<TResponse>
where
    TRequest: prost::Message,
    TResponse: prost::Message + Default,
{
    let mut buf = Vec::new();
    request.encode(&mut buf).map_err(|e| StdError::msg(format!("Encode: {}", e)))?;
    let bytes = query_grpc(deps, path, Binary::from(buf))?;
    TResponse::decode(bytes.as_slice()).map_err(|e| StdError::msg(format!("Decode: {}", e)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use cosmwasm_std::testing::{mock_dependencies, mock_env, MockApi};
    use cosmwasm_std::{from_json, Addr, MessageInfo};

    fn mock_instantiate_msg(api: &MockApi) -> InstantiateMsg {
        InstantiateMsg {
            admin: api.addr_make("admin").to_string(),
            buyer: api.addr_make("buyer").to_string(),
            accepted_chain_id: "ethereum".to_string(),
            accepted_eth_contract: "0xdac17f958d2ee523a2206206994597c13d831ec7".to_string(),
            price_usd: Uint128::from(25000u128), // $0.025
        }
    }

    #[test]
    fn proper_instantiation() {
        let deps = mock_dependencies();
        let api = MockApi::default();
        let buyer_addr = api.addr_make("buyer").to_string();
        
        let mut deps = deps;
        let env = mock_env();
        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![],
        };

        let res = instantiate(deps.as_mut(), env, info, mock_instantiate_msg(&api)).unwrap();
        assert!(res.attributes.iter().any(|a| a.key == "buyer" && a.value == buyer_addr));
        assert!(res.attributes.iter().any(|a| a.key == "accepted_chain_id" && a.value == "ethereum"));
        assert!(res.attributes.iter().any(|a| a.key == "accepted_eth_contract" && a.value == "0xdac17f958d2ee523a2206206994597c13d831ec7"));
    }

    #[test]
    fn test_pause_resume() {
        let deps = mock_dependencies();
        let api = MockApi::default();
        let admin_addr = api.addr_make("admin");
        
        let mut deps = deps;
        let env = mock_env();
        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![],
        };
        instantiate(deps.as_mut(), env.clone(), info, mock_instantiate_msg(&api)).unwrap();

        let info = MessageInfo {
            sender: admin_addr.clone(),
            funds: vec![],
        };
        execute(deps.as_mut(), env.clone(), info.clone(), ExecuteMsg::Pause {}).unwrap();

        let config: ConfigResponse =
            from_json(&query(deps.as_ref(), env.clone(), QueryMsg::Config {}).unwrap()).unwrap();
        assert!(config.is_paused);

        execute(deps.as_mut(), env.clone(), info, ExecuteMsg::Resume {}).unwrap();
        let config: ConfigResponse =
            from_json(&query(deps.as_ref(), env, QueryMsg::Config {}).unwrap()).unwrap();
        assert!(!config.is_paused);
    }

    #[test]
    fn test_update_buyer() {
        let deps = mock_dependencies();
        let api = MockApi::default();
        let admin_addr = api.addr_make("admin");
        let new_buyer = api.addr_make("newbuyer").to_string();
        
        let mut deps = deps;
        let env = mock_env();
        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![],
        };
        instantiate(deps.as_mut(), env.clone(), info, mock_instantiate_msg(&api)).unwrap();

        let info = MessageInfo {
            sender: admin_addr,
            funds: vec![],
        };
        execute(
            deps.as_mut(),
            env.clone(),
            info,
            ExecuteMsg::UpdateBuyer { buyer: new_buyer.clone() },
        )
        .unwrap();

        let config: ConfigResponse =
            from_json(&query(deps.as_ref(), env, QueryMsg::Config {}).unwrap()).unwrap();
        assert_eq!(config.buyer, new_buyer);
    }

    #[test]
    fn test_update_price() {
        let deps = mock_dependencies();
        let api = MockApi::default();
        let admin_addr = api.addr_make("admin");
        
        let mut deps = deps;
        let env = mock_env();
        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![],
        };
        instantiate(deps.as_mut(), env.clone(), info, mock_instantiate_msg(&api)).unwrap();

        let info = MessageInfo {
            sender: admin_addr,
            funds: vec![],
        };
        execute(
            deps.as_mut(),
            env.clone(),
            info,
            ExecuteMsg::UpdatePrice { price_usd: Uint128::from(50000u128) },
        )
        .unwrap();

        let config: ConfigResponse =
            from_json(&query(deps.as_ref(), env, QueryMsg::Config {}).unwrap()).unwrap();
        assert_eq!(config.price_usd, Uint128::from(50000u128));
    }

    #[test]
    fn test_calculate_tokens() {
        let deps = mock_dependencies();
        let api = MockApi::default();
        
        let mut deps = deps;
        let env = mock_env();
        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![],
        };
        instantiate(deps.as_mut(), env.clone(), info, mock_instantiate_msg(&api)).unwrap();

        let usd_amount = Uint128::from(100_000_000u128); // $100
        let response: TokenCalculationResponse = from_json(
            &query(deps.as_ref(), env, QueryMsg::CalculateTokens { usd_amount }).unwrap(),
        )
        .unwrap();

        assert_eq!(response.tokens, Uint128::from(4_000_000_000_000u128));
        assert_eq!(response.price_usd, Uint128::from(25000u128));
    }

    #[test]
    fn test_unauthorized_update() {
        let deps = mock_dependencies();
        let api = MockApi::default();
        let attacker = api.addr_make("attacker");
        let hacker = api.addr_make("hacker").to_string();
        
        let mut deps = deps;
        let env = mock_env();
        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![],
        };
        instantiate(deps.as_mut(), env.clone(), info, mock_instantiate_msg(&api)).unwrap();

        let info = MessageInfo {
            sender: attacker,
            funds: vec![],
        };
        let err = execute(
            deps.as_mut(),
            env,
            info,
            ExecuteMsg::UpdateBuyer { buyer: hacker },
        )
        .unwrap_err();
        assert!(matches!(err, ContractError::Unauthorized {}));
    }
}
