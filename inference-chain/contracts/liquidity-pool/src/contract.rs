use cosmwasm_std::{
    entry_point, from_json, to_json_binary, to_json_vec, BankMsg, Binary, Coin, Deps, DepsMut, Env, MessageInfo, Response,
    StdError, StdResult, Uint128, QueryRequest, StakingQuery, GrpcQuery, ContractResult, SystemResult, WasmMsg,
};
use prost::Message; // For proto encoding/decoding
use cw2::{get_contract_version, set_contract_version};

use crate::error::ContractError;
use crate::msg::{
    ConfigResponse, Cw20ReceiveMsg, DailyStatsResponse, ExecuteMsg, InstantiateMsg,
    NativeBalanceResponse, PricingInfoResponse, PurchaseTokenMsg, QueryMsg, 
    TestBridgeValidationResponse, TokenCalculationResponse, BlockHeightResponse,
    ApprovedTokensForTradeJson, ApprovedTokenJson,
};
use crate::state::{
    calculate_current_price, calculate_current_tier, calculate_tokens_for_usd, calculate_multi_tier_purchase,
    Config, DailyStats, PricingConfig,
    CONFIG, DAILY_STATS, PRICING_CONFIG,
};

// Proto message types for gRPC query
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

// Proto types for ApprovedTokensForTrade response decoding (for gRPC path)
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

// Empty request for endpoints without fields
#[derive(Clone, PartialEq, Message)]
pub struct EmptyRequest {}

// Proto types for bank TotalSupply query (to get base denom)
#[derive(Clone, PartialEq, Message)]
pub struct QueryTotalSupplyRequest {
    // Pagination is optional and omitted - we just need the first coin
}

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

const CONTRACT_NAME: &str = "inference-liquidity-pool";
const CONTRACT_VERSION: &str = env!("CARGO_PKG_VERSION");

// Helper function to validate if a token is a legitimate bridge token for trading
// Accepts either a raw CW20 address (bech32) or a value prefixed with "cw20:"
fn validate_wrapped_token_for_trade(deps: Deps, token_identifier: &str) -> Result<bool, ContractError> {
    deps.api.debug(&format!(
        "LP: validate_wrapped_token_for_trade start token_identifier={}",
        token_identifier
    ));

    // For compatibility: allow both "cw20:<bech32>" and raw bech32 addresses
    let contract_address = token_identifier
        .strip_prefix("cw20:")
        .unwrap_or(token_identifier);
    deps.api.debug(&format!(
        "LP: extracted cw20 contract_address={}",
        contract_address
    ));

    // Construct the proto request and send via generic helper
    let request = QueryValidateWrappedTokenForTradeRequest {
        contract_address: contract_address.to_string(),
    };
    deps.api.debug("LP: issuing query_grpc for ValidateWrappedTokenForTrade");
    let response: QueryValidateWrappedTokenForTradeResponse = query_proto(
        deps,
        "/inference.inference.Query/ValidateWrappedTokenForTrade",
        &request,
    )
    .map_err(|e| ContractError::Std(e))?;
    deps.api.debug(&format!(
        "LP: ValidateWrappedTokenForTrade response is_valid={}",
        response.is_valid
    ));

    Ok(response.is_valid)
}

// Helper function to get native denomination from bank module
fn get_native_denom(deps: Deps) -> Result<String, ContractError> {
    // Query the bank module's total supply to get the base/native denomination
    // The first coin in total supply is typically the native/base denom
    let request = QueryTotalSupplyRequest {};
    
    match query_proto::<QueryTotalSupplyRequest, QueryTotalSupplyResponse>(
        deps,
        "/cosmos.bank.v1beta1.Query/TotalSupply",
        &request,
    ) {
        Ok(response) => {
            // Get the first coin from total supply, which is the native/base denom
            if let Some(coin) = response.supply.first() {
                if !coin.denom.is_empty() {
                    return Ok(coin.denom.clone());
                }
            }
            // Fall back to default if supply is empty or denom is empty
            Ok("ngonka".to_string())
        },
        Err(_) => {
            // Fall back to default if query fails
            Ok("ngonka".to_string())
        }
    }
}

// Helper function to create CW20 transfer message
fn create_cw20_transfer_msg(
    cw20_contract: String,
    recipient: String,
    amount: Uint128,
) -> Result<WasmMsg, ContractError> {
    let transfer_msg_str = format!(
        r#"{{"transfer":{{"recipient":"{}","amount":"{}"}}}}"#,
        recipient,
        amount
    );
    
    Ok(WasmMsg::Execute {
        contract_addr: cw20_contract,
        msg: Binary::from(transfer_msg_str.as_bytes()),
        funds: vec![],
    })
}

#[entry_point]
pub fn instantiate(
    deps: DepsMut,
    env: Env,
    _info: MessageInfo,
    msg: InstantiateMsg,
) -> Result<Response, ContractError> {
    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)
        .map_err(|e| ContractError::Std(cosmwasm_std::StdError::msg(e.to_string())))?;

    // Validate daily limit
    let daily_limit_bp = msg.daily_limit_bp.unwrap_or(Uint128::from(100u128));
    if daily_limit_bp.is_zero() || daily_limit_bp > Uint128::from(10000u128) {
        return Err(ContractError::InvalidBasisPoints {
            value: daily_limit_bp,
        });
    }

    // Handle optional admin
    let admin = match msg.admin {
        Some(ref addr) if !addr.is_empty() => deps.api.addr_validate(addr)?.to_string(),
        _ => String::new(), // No admin
    };

    // Get native denomination from chain
    let native_denom = get_native_denom(deps.as_ref())?;

    // Use provided total_supply or default to 0
    let total_supply = msg.total_supply.unwrap_or(Uint128::zero());

    let config = Config {
        admin: admin.clone(),
        native_denom: native_denom.clone(),
        daily_limit_bp: daily_limit_bp,
        is_paused: false,
        total_supply: total_supply,
        total_tokens_sold: Uint128::zero(),
    };

    CONFIG.save(deps.storage, &config)?;

    // Use defaults for pricing fields if None
    let pricing_config = PricingConfig {
        base_price_usd: msg.base_price_usd.unwrap_or(Uint128::from(25000u128)),
        tokens_per_tier: msg.tokens_per_tier.unwrap_or(Uint128::from(3_000_000_000_000_000u128)),
        tier_multiplier: msg.tier_multiplier.unwrap_or(Uint128::from(1300u128)),
    };

    PRICING_CONFIG.save(deps.storage, &pricing_config)?;

    // Initialize daily stats
    let current_day = env.block.time.seconds() / 86400;
    let daily_stats = DailyStats {
        current_day,
        usd_received_today: Uint128::zero(),
        tokens_sold_today: Uint128::zero(),
    };
    DAILY_STATS.save(deps.storage, &daily_stats)?;

    Ok(Response::new()
        .add_attribute("method", "instantiate")
        .add_attribute("admin", admin)
        .add_attribute("native_denom", native_denom)
        .add_attribute("total_supply", total_supply))
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
        ExecuteMsg::UpdateDailyLimit { daily_limit_bp } => {
            update_daily_limit(deps, info, daily_limit_bp)
        }
        ExecuteMsg::WithdrawNativeTokens { amount, recipient } => {
            withdraw_native_tokens(deps, info, amount, recipient)
        }
        ExecuteMsg::EmergencyWithdraw { recipient } => emergency_withdraw(deps, env, info, recipient),
        ExecuteMsg::UpdatePricingConfig {
            base_price_usd,
            tokens_per_tier,
            tier_multiplier,
        } => update_pricing_config(deps, info, base_price_usd, tokens_per_tier, tier_multiplier),
        ExecuteMsg::AddPaymentToken { denom, usd_rate } => {
            add_payment_token(deps, info, denom, usd_rate)
        }
        ExecuteMsg::RemovePaymentToken { denom } => remove_payment_token(deps, info, denom),
    }
}

// Handle receiving CW20 tokens (wrapped bridge tokens only)
fn receive_cw20(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    cw20_msg: Cw20ReceiveMsg,
) -> Result<Response, ContractError> {
    deps.api.debug(&format!(
        "LP: receive_cw20 start from_cw20={} buyer={} amount={} msg_len={}",
        info.sender,
        cw20_msg.sender,
        cw20_msg.amount,
        cw20_msg.msg.len()
    ));
    let config = CONFIG.load(deps.storage)?;
    let pricing_config = PRICING_CONFIG.load(deps.storage)?;

    if config.is_paused {
        return Err(ContractError::ContractPaused {});
    }

    // The sender (info.sender) is the CW20 contract address
    let cw20_contract = info.sender.to_string();
    deps.api.debug(&format!(
        "LP: validating wrapped token via chain for cw20={}",
        cw20_contract
    ));
    
    // CRITICAL: Validate this is a legitimate bridge token for trading by checking the cosmos module
    if !validate_wrapped_token_for_trade(deps.as_ref(), &cw20_contract)? {
        deps.api.debug("LP: validate_wrapped_token_for_trade returned false");
        return Err(ContractError::TokenNotAccepted {
            token: format!("CW20 contract {} is not a legitimate bridge token approved for trading", cw20_contract),
        });
    }
    deps.api.debug("LP: validate_wrapped_token_for_trade returned true");

    // Parse the message to determine what action to take
    deps.api.debug("LP: parsing inner purchase msg");
    let _purchase_msg: PurchaseTokenMsg = from_json(&cw20_msg.msg)?;
    
    // The actual sender of the tokens (the user)
    let buyer = cw20_msg.sender;
    let token_amount = cw20_msg.amount;

    let current_day = env.block.time.seconds() / 86400;
    let mut daily_stats = DAILY_STATS.load(deps.storage)?;

    // Reset daily stats if it's a new day
    if daily_stats.current_day != current_day {
        daily_stats.current_day = current_day;
        daily_stats.usd_received_today = Uint128::zero();
        daily_stats.tokens_sold_today = Uint128::zero();
    }

    // For wrapped bridge tokens, treat amount as micro-USD (1:1 with amount)
    // This assumes wrapped tokens like USDT have 6 decimals and are USD-pegged
    let usd_value = token_amount;

    if usd_value.is_zero() {
        return Err(ContractError::ZeroAmount {});
    }

    // Calculate multi-tier purchase: handles purchases spanning multiple tiers
    let (tokens_to_buy, actual_usd_to_spend, start_tier, end_tier, average_price) = calculate_multi_tier_purchase(
        usd_value,
        config.total_tokens_sold,
        &pricing_config,
    );

    // Verify we can spend ALL the USD received (no partial spending allowed)
    if actual_usd_to_spend != usd_value {
        deps.api.debug(&format!(
            "LP: Cannot spend full USD amount - requested: {}, can spend: {}",
            usd_value, actual_usd_to_spend
        ));
        // This shouldn't happen with proper multi-tier calculation, but safety check
        return Err(ContractError::Std(StdError::msg(
            format!("Cannot process full USD amount: requested {}, can only process {}", 
                    usd_value, actual_usd_to_spend)
        )));
    }

    if tokens_to_buy.is_zero() {
        return Err(ContractError::ZeroAmount {});
    }

    // Check daily limit - pure token-based approach
    let daily_token_limit = match config
        .total_supply
        .checked_mul(Uint128::from(config.daily_limit_bp))
    {
        Ok(amount) => match amount.checked_div(Uint128::from(10000u128)) {
            Ok(limit) => limit,
            Err(_) => return Err(ContractError::InvalidBasisPoints {
                value: config.daily_limit_bp,
            }),
        },
        Err(_) => return Err(ContractError::InvalidBasisPoints {
            value: config.daily_limit_bp,
        }),
    };

    let tokens_available_today = daily_token_limit
        .checked_sub(daily_stats.tokens_sold_today)
        .unwrap_or_default();

    // Check daily limit: reject if exceeds available (no partial fills in CW20)
    if tokens_to_buy > tokens_available_today {
        return Err(ContractError::DailyLimitExceeded {
            available: tokens_available_today.u128(),
            requested: tokens_to_buy.u128(),
        });
    }

    // We're spending ALL the USD received (verified above)
    let usd_amount_to_track = usd_value;

    // Check contract balance
    deps.api.debug("LP: querying contract native balance");
    let contract_balance = deps
        .querier
        .query_balance(env.contract.address.to_string(), config.native_denom.as_str())?;

    // Convert Uint256 balance to Uint128 for comparison
    let contract_balance_amount_128: Uint128 = contract_balance
        .amount
        .try_into()
        .map_err(|_| ContractError::Std(cosmwasm_std::StdError::msg("contract balance exceeds Uint128")))?;

    if tokens_to_buy > contract_balance_amount_128 {
        return Err(ContractError::InsufficientBalance {
            available: contract_balance_amount_128.u128(),
            needed: tokens_to_buy.u128(),
        });
    }

    // Update daily stats with both USD and token tracking
    daily_stats.usd_received_today = daily_stats
        .usd_received_today
        .checked_add(usd_amount_to_track)
        .map_err(|e| ContractError::Std(cosmwasm_std::StdError::msg(format!("overflow: {}", e))))?;
    
    daily_stats.tokens_sold_today = daily_stats
        .tokens_sold_today
        .checked_add(tokens_to_buy)
        .map_err(|e| ContractError::Std(cosmwasm_std::StdError::msg(format!("overflow: {}", e))))?;
    
    let mut updated_config = config;
    // Update total tokens sold (for tier calculation)
    updated_config.total_tokens_sold = updated_config
        .total_tokens_sold
        .checked_add(tokens_to_buy)
        .map_err(|e| ContractError::Std(cosmwasm_std::StdError::msg(format!("overflow: {}", e))))?;

    DAILY_STATS.save(deps.storage, &daily_stats)?;
    CONFIG.save(deps.storage, &updated_config)?;

    // Send native tokens to buyer
    let send_native_msg = BankMsg::Send {
        to_address: buyer.clone(),
        amount: vec![Coin {
            denom: updated_config.native_denom.clone(),
            amount: tokens_to_buy.into(),
        }],
    };

    // Forward received CW20 tokens to governance module (admin)
    let mut response = Response::new().add_message(send_native_msg);
    
    if !updated_config.admin.is_empty() {
        let transfer_cw20_msg = create_cw20_transfer_msg(
            cw20_contract.clone(),
            updated_config.admin.clone(),
            token_amount,
        )?;
        response = response.add_message(transfer_cw20_msg);
        deps.api.debug(&format!(
            "LP: forwarding CW20 tokens to governance admin={} amount={}",
            updated_config.admin,
            token_amount
        ));
    } else {
        deps.api.debug("LP: no admin set, CW20 tokens remain in contract");
    }

    deps.api.debug("LP: building success response with native send and CW20 forward");
    
    Ok(response
        .add_attribute("method", "purchase_with_wrapped_token")
        .add_attribute("buyer", buyer)
        .add_attribute("wrapped_token_contract", cw20_contract)
        .add_attribute("wrapped_token_amount", token_amount)
        .add_attribute("tokens_purchased", tokens_to_buy)
        .add_attribute("usd_received", usd_value)
        .add_attribute("usd_spent", usd_amount_to_track)
        .add_attribute("start_tier", start_tier.to_string())
        .add_attribute("end_tier", end_tier.to_string())
        .add_attribute("average_price_paid", average_price)
        .add_attribute("tokens_available_today", tokens_available_today)
        .add_attribute("cw20_forwarded_to", updated_config.admin))
}

fn pause_contract(deps: DepsMut, info: MessageInfo) -> Result<Response, ContractError> {
    let mut config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }

    config.is_paused = true;
    CONFIG.save(deps.storage, &config)?;

    Ok(Response::new()
        .add_attribute("method", "pause")
        .add_attribute("admin", info.sender))
}

fn resume_contract(deps: DepsMut, info: MessageInfo) -> Result<Response, ContractError> {
    let mut config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }

    config.is_paused = false;
    CONFIG.save(deps.storage, &config)?;

    Ok(Response::new()
        .add_attribute("method", "resume")
        .add_attribute("admin", info.sender))
}

fn update_daily_limit(
    deps: DepsMut,
    info: MessageInfo,
    daily_limit_bp: Option<Uint128>,
) -> Result<Response, ContractError> {
    let mut config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }

    let daily_limit_bp = daily_limit_bp.unwrap_or(Uint128::from(100u128));
    if daily_limit_bp.is_zero() || daily_limit_bp > Uint128::from(10000u128) {
        return Err(ContractError::InvalidBasisPoints {
            value: daily_limit_bp,
        });
    }

    config.daily_limit_bp = daily_limit_bp;
    CONFIG.save(deps.storage, &config)?;

    Ok(Response::new()
        .add_attribute("method", "update_daily_limit")
        .add_attribute("new_limit_bp", daily_limit_bp.to_string())
        .add_attribute("admin", info.sender))
}

fn withdraw_native_tokens(
    deps: DepsMut,
    info: MessageInfo,
    amount: Uint128,
    recipient: String,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
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
        .add_attribute("method", "withdraw_native_tokens")
        .add_attribute("amount", amount)
        .add_attribute("recipient", recipient)
        .add_attribute("admin", info.sender))
}

fn emergency_withdraw(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    recipient: String,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }

    let recipient_addr = deps.api.addr_validate(&recipient)?;

    // Get all balances (only native denom is used here)
    let balance = deps
        .querier
        .query_balance(env.contract.address.to_string(), config.native_denom.clone())?;

    if balance.amount.is_zero() {
        return Ok(Response::new()
            .add_attribute("method", "emergency_withdraw")
            .add_attribute("message", "no_funds_to_withdraw"));
    }

    let send_msg = BankMsg::Send {
        to_address: recipient_addr.to_string(),
        amount: vec![balance.clone()],
    };

    Ok(Response::new()
        .add_message(send_msg)
        .add_attribute("method", "emergency_withdraw")
        .add_attribute("recipient", recipient)
        .add_attribute("withdrawn_funds", format!("{:?}", balance))
        .add_attribute("admin", info.sender))
}

fn update_pricing_config(
    deps: DepsMut,
    info: MessageInfo,
    base_price_usd: Option<Uint128>,
    tokens_per_tier: Option<Uint128>,
    tier_multiplier: Option<Uint128>,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }

    let mut pricing_config = PRICING_CONFIG.load(deps.storage)?;

    if let Some(price) = base_price_usd {
        if price.is_zero() {
            return Err(ContractError::ZeroAmount {});
        }
        pricing_config.base_price_usd = price;
    }

    if let Some(tokens) = tokens_per_tier {
        if tokens.is_zero() {
            return Err(ContractError::ZeroAmount {});
        }
        pricing_config.tokens_per_tier = tokens;
    }

    if let Some(multiplier) = tier_multiplier {
        if multiplier.is_zero() {
            return Err(ContractError::InvalidExchangeRate {
                token: "tier_multiplier must be > 0 (1.0x)".to_string(),
            });
        }
        pricing_config.tier_multiplier = multiplier;
    }

    PRICING_CONFIG.save(deps.storage, &pricing_config)?;

    Ok(Response::new()
        .add_attribute("method", "update_pricing_config")
        .add_attribute("admin", info.sender))
}

fn add_payment_token(
    deps: DepsMut,
    info: MessageInfo,
    denom: String,
    usd_rate: Uint128,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }

    if usd_rate.is_zero() {
        return Err(ContractError::InvalidExchangeRate { token: denom });
    }

    // CRITICAL SECURITY CHECK: Verify this is a legitimate bridge token for trading
    if !validate_wrapped_token_for_trade(deps.as_ref(), &denom)? {
        return Err(ContractError::TokenNotAccepted {
            token: format!("Token {} is not a legitimate bridge token approved for trading", denom),
        });
    }

    // PAYMENT_TOKENS.save(deps.storage, denom.clone(), &usd_rate)?; // This line is removed

    Ok(Response::new()
        .add_attribute("method", "add_payment_token")
        .add_attribute("token", denom)
        .add_attribute("usd_rate", usd_rate)
        .add_attribute("bridge_token_validated", "true")
        .add_attribute("admin", info.sender))
}

fn remove_payment_token(
    deps: DepsMut,
    info: MessageInfo,
    denom: String,
) -> Result<Response, ContractError> {
    let config = CONFIG.load(deps.storage)?;

    if config.admin.is_empty() || info.sender.as_str() != config.admin {
        return Err(ContractError::Unauthorized {});
    }

    // PAYMENT_TOKENS.remove(deps.storage, denom.clone()); // This line is removed

    Ok(Response::new()
        .add_attribute("method", "remove_payment_token")
        .add_attribute("token", denom)
        .add_attribute("admin", info.sender))
}

#[entry_point]
pub fn query(deps: Deps, env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::Config {} => to_json_binary(&query_config(deps)?),
        QueryMsg::DailyStats {} => to_json_binary(&query_daily_stats(deps, env)?),
        QueryMsg::NativeBalance {} => to_json_binary(&query_native_balance(deps, env)?),
        QueryMsg::PricingInfo {} => to_json_binary(&query_pricing_info(deps)?),
        QueryMsg::CalculateTokens { usd_amount } => {
            to_json_binary(&query_calculate_tokens(deps, usd_amount)?)
        }
        QueryMsg::TestBridgeValidation { cw20_contract } => {
            to_json_binary(&query_test_bridge_validation(deps, cw20_contract)?)
        }
        QueryMsg::BlockHeight {} => {
            to_json_binary(&query_block_height(env)?)
        }
        QueryMsg::TestApprovedTokens {} => {
            to_json_binary(&query_test_approved_tokens(deps)?)
        }
    }
}

#[entry_point]
pub fn migrate(
    deps: DepsMut,
    _env: Env,
    _msg: Binary,
) -> Result<Response, ContractError> {
    let old = get_contract_version(deps.storage)
        .map_err(|e| ContractError::Std(cosmwasm_std::StdError::msg(e.to_string())))?;
    if old.contract != CONTRACT_NAME {
        return Err(ContractError::Std(StdError::msg(format!(
            "wrong contract: expected {} got {}",
            CONTRACT_NAME, old.contract
        ))));
    }

    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)
        .map_err(|e| ContractError::Std(cosmwasm_std::StdError::msg(e.to_string())))?;

    // Update stored native_denom to the correct value from chain
    // This fixes any incorrect stored values and avoids expensive queries on every execution
    let mut config = CONFIG.load(deps.storage)?;
    let correct_native_denom = get_native_denom(deps.as_ref())?;
    if config.native_denom != correct_native_denom {
        config.native_denom = correct_native_denom.clone();
        CONFIG.save(deps.storage, &config)?;
    }

    Ok(Response::new()
        .add_attribute("action", "migrate")
        .add_attribute("from_version", old.version)
        .add_attribute("to_version", CONTRACT_VERSION))
}

fn query_config(deps: Deps) -> StdResult<ConfigResponse> {
    let config = CONFIG.load(deps.storage)?;
    Ok(ConfigResponse {
        admin: config.admin,
        native_denom: config.native_denom,
        daily_limit_bp: config.daily_limit_bp,
        is_paused: config.is_paused,
        total_tokens_sold: config.total_tokens_sold,
    })
}

fn query_test_bridge_validation(deps: Deps, cw20_contract: String) -> StdResult<TestBridgeValidationResponse> {
    // Accept either raw cw20 address or prefixed cw20:<addr>
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
    // Empty request protobuf
    let decoded: QueryApprovedTokensForTradeResponseProto = query_proto(
        deps,
        "/inference.inference.Query/ApprovedTokensForTrade",
        &EmptyRequest::default(),
    )?;
    let approved_tokens = decoded
        .approved_tokens
        .into_iter()
        .map(|t| ApprovedTokenJson { chain_id: t.chain_id, contract_address: t.contract_address })
        .collect();
    Ok(ApprovedTokensForTradeJson { approved_tokens })
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
        .map_err(|e| StdError::msg(format!("Serializing QueryRequest: {e}")))?;
    match deps.querier.raw_query(&raw) {
        SystemResult::Err(system_err) => Err(StdError::msg(format!(
            "Querier system error: {system_err}"
        ))),
        SystemResult::Ok(ContractResult::Err(contract_err)) => Err(StdError::msg(
            format!("Querier contract error: {contract_err}")
        )),
        SystemResult::Ok(ContractResult::Ok(value)) => Ok(value),
    }
}

// Generic helper: encode request proto and decode response proto
fn query_proto<TRequest, TResponse>(deps: Deps, path: &str, request: &TRequest) -> StdResult<TResponse>
where
    TRequest: prost::Message,
    TResponse: prost::Message + Default,
{
    let mut buf = Vec::new();
    request
        .encode(&mut buf)
        .map_err(|e| StdError::msg(format!("Encode request: {}", e)))?;
    let bytes = query_grpc(deps, path, Binary::from(buf))?;
    TResponse::decode(bytes.as_slice())
        .map_err(|e| StdError::msg(format!("Decode response: {}", e)))
}

fn query_daily_stats(deps: Deps, env: Env) -> StdResult<DailyStatsResponse> {
    let config = CONFIG.load(deps.storage)?;
    let mut daily_stats = DAILY_STATS.load(deps.storage)?;

    let current_day = env.block.time.seconds() / 86400;

    // Reset if new day
    if daily_stats.current_day != current_day {
        daily_stats.current_day = current_day;
        daily_stats.usd_received_today = Uint128::zero();
        daily_stats.tokens_sold_today = Uint128::zero();
    }

    let daily_token_limit = config
        .total_supply
        .checked_mul(Uint128::from(config.daily_limit_bp))
        .map(|x| x.checked_div(Uint128::from(10000u128)).unwrap_or_default())
        .unwrap_or_default();

    let tokens_available_today = daily_token_limit
        .checked_sub(daily_stats.tokens_sold_today)
        .unwrap_or_default();

    Ok(DailyStatsResponse {
        current_day: daily_stats.current_day,
        usd_received_today: daily_stats.usd_received_today,
        tokens_sold_today: daily_stats.tokens_sold_today,
        tokens_available_today,
        daily_token_limit,
        total_supply: config.total_supply,
    })
}

fn query_native_balance(deps: Deps, env: Env) -> StdResult<NativeBalanceResponse> {
    let config = CONFIG.load(deps.storage)?;
    let balance = deps
        .querier
        .query_balance(&env.contract.address, &config.native_denom)?;

    Ok(NativeBalanceResponse { balance })
}

fn query_pricing_info(deps: Deps) -> StdResult<PricingInfoResponse> {
    let config = CONFIG.load(deps.storage)?;
    let pricing_config = PRICING_CONFIG.load(deps.storage)?;

    let current_tier = calculate_current_tier(config.total_tokens_sold, pricing_config.tokens_per_tier);
    let current_price = calculate_current_price(
        pricing_config.base_price_usd,
        current_tier,
        pricing_config.tier_multiplier,
    );

    // Calculate next tier info - token count needed for next tier
    let next_tier_at = pricing_config.tokens_per_tier.checked_mul(Uint128::from((current_tier + 1) as u128)).unwrap_or(Uint128::zero());
    let next_tier_price = calculate_current_price(
        pricing_config.base_price_usd,
        current_tier + 1,
        pricing_config.tier_multiplier,
    );

    Ok(PricingInfoResponse {
        current_tier,
        current_price_usd: current_price,
        total_tokens_sold: config.total_tokens_sold,
        tokens_per_tier: pricing_config.tokens_per_tier,
        base_price_usd: pricing_config.base_price_usd,
        tier_multiplier: pricing_config.tier_multiplier,
        next_tier_at,
        next_tier_price,
    })
}

fn query_calculate_tokens(deps: Deps, usd_amount: Uint128) -> StdResult<TokenCalculationResponse> {
    let config = CONFIG.load(deps.storage)?;
    let pricing_config = PRICING_CONFIG.load(deps.storage)?;

    let current_tier = calculate_current_tier(config.total_tokens_sold, pricing_config.tokens_per_tier);
    let current_price = calculate_current_price(
        pricing_config.base_price_usd,
        current_tier,
        pricing_config.tier_multiplier,
    );

    let tokens = calculate_tokens_for_usd(usd_amount, current_price);

    Ok(TokenCalculationResponse {
        tokens,
        current_price,
        current_tier,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use cosmwasm_std::testing::{mock_dependencies, mock_env};
    use cosmwasm_std::{coins, from_json, Addr, MessageInfo};
    use std::collections::HashMap;

    #[test]
    fn proper_instantiation() {
        let mut deps = mock_dependencies();
        let env = mock_env();

        let msg = InstantiateMsg {
            admin: Some("admin".to_string()),
            daily_limit_bp: Some(Uint128::from(100u128)), // 1%
            base_price_usd: Some(Uint128::from(25000u128)), // $0.025 with 6 decimals for USD
            tokens_per_tier: Some(Uint128::from(3_000_000_000_000_000u128)), // 3 million tokens (9 decimals)
            tier_multiplier: Some(Uint128::from(1300u128)), // 1.3x
            total_supply: Some(Uint128::from(120_000_000_000_000_000u128)), // 120M tokens
        };

        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![], // same as &[] before
        };
        let res = instantiate(deps.as_mut(), env, info, msg).unwrap();

        assert_eq!(res.attributes.len(), 4);
    }

    #[test]
    fn test_pause_resume() {
        let mut deps = mock_dependencies();
        let env = mock_env();

        // Instantiate
        let msg = InstantiateMsg {
            admin: Some("admin".to_string()),
            daily_limit_bp: Some(Uint128::from(100u128)),
            base_price_usd: Some(Uint128::from(25000u128)), // $0.025 with 6 decimals for USD
            tokens_per_tier: Some(Uint128::from(3_000_000_000_000_000u128)), // 3 million tokens (9 decimals)
            tier_multiplier: Some(Uint128::from(1300u128)), // 1.3x
            total_supply: Some(Uint128::from(120_000_000_000_000_000u128)), // 120M tokens
        };

        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![], // same as &[] before
        };
        instantiate(deps.as_mut(), env.clone(), info, msg).unwrap();

        // Pause
        let pause_msg = ExecuteMsg::Pause {};
        let info = MessageInfo {
            sender: Addr::unchecked("admin"),
            funds: vec![], // same as &[] before
        };
        execute(deps.as_mut(), env.clone(), info, pause_msg).unwrap();

        // Check config
        let config: ConfigResponse =
            from_json(&query(deps.as_ref(), env.clone(), QueryMsg::Config {}).unwrap()).unwrap();
        assert!(config.is_paused);

        // Resume
        let resume_msg = ExecuteMsg::Resume {};
        let info = MessageInfo {
            sender: Addr::unchecked("admin"),
            funds: vec![], // same as &[] before
        };
        execute(deps.as_mut(), env.clone(), info, resume_msg).unwrap();

        // Check config
        let config: ConfigResponse =
            from_json(&query(deps.as_ref(), env, QueryMsg::Config {}).unwrap()).unwrap();
        assert!(!config.is_paused);
    }

    #[test]
    fn test_usd_based_tier_calculation() {
        let mut deps = mock_dependencies();
        let env = mock_env();

        // Instantiate with known values
        let msg = InstantiateMsg {
            admin: Some("admin".to_string()),
            daily_limit_bp: Some(Uint128::from(1000u128)), // 10%
            base_price_usd: Some(Uint128::from(25000u128)), // $0.025 with 6 decimals for USD
            tokens_per_tier: Some(Uint128::from(3_000_000_000_000_000u128)), // 3 million tokens per tier (9 decimals)
            tier_multiplier: Some(Uint128::from(1300u128)), // 1.3x
            total_supply: Some(Uint128::from(120_000_000_000_000_000u128)), // 120M tokens
        };

        let info = MessageInfo {
            sender: Addr::unchecked("creator"),
            funds: vec![], // same as &[] before
        };
        instantiate(deps.as_mut(), env.clone(), info, msg).unwrap();

        // Test tier calculation for $100 USD (100,000,000 micro-units)
        let usd_amount = Uint128::from(100_000_000u128); // $100
        let response: TokenCalculationResponse = from_json(
            &query(deps.as_ref(), env.clone(), QueryMsg::CalculateTokens { usd_amount }).unwrap()
        ).unwrap();

        // With $0.025 base price and 10M tokens per tier:
        // USD per tier = 10,000,000 * 25,000 = 250,000,000,000 micro-USD = $250,000
        // $100 should be in tier 0 (before first tier)
        assert_eq!(response.current_tier, 0);
        assert_eq!(response.current_price, Uint128::from(25000u128)); // $0.025
        assert_eq!(response.tokens, Uint128::from(4_000_000_000u128)); // 4000 tokens for $100 (100,000,000 * 1,000,000 / 25,000)
    }

    #[test]
    fn test_multi_tier_purchase() {
        use crate::state::{calculate_multi_tier_purchase, PricingConfig};

        // Test setup: 3M tokens per tier, $0.025 base price, 1.3x multiplier (token-based tiers)
        let pricing_config = PricingConfig {
            base_price_usd: Uint128::from(25000u128), // $0.025
            tokens_per_tier: Uint128::from(3_000_000_000_000_000u128), // 3M tokens with 9 decimals
            tier_multiplier: Uint128::from(1300u128), // 1.3x multiplier
        };

        // Test 1: Purchase within single tier
        let (tokens, usd_spent, start_tier, end_tier, avg_price) = calculate_multi_tier_purchase(
            Uint128::from(100_000_000u128), // $100
            Uint128::zero(), // No tokens sold yet
            &pricing_config,
        );
        // Should get 4000 tokens at $0.025 each
        assert_eq!(tokens, Uint128::from(4_000_000_000_000u128)); // 4000 tokens (with 9 decimals)
        assert_eq!(usd_spent, Uint128::from(100_000_000u128)); // $100
        assert_eq!(start_tier, 0);
        assert_eq!(end_tier, 0); // Still in same tier
        assert_eq!(avg_price, Uint128::from(25000u128)); // $0.025

        // Test 2: Purchase spanning two tiers
        // Start with 2.5M tokens already sold (very close to tier boundary of 3M tokens)
        // Use $20,000 to ensure we cross into tier 1
        let (tokens, usd_spent, start_tier, end_tier, avg_price) = calculate_multi_tier_purchase(
            Uint128::from(20_000_000_000u128), // $20,000 purchase
            Uint128::from(2_500_000_000_000_000u128), // 2.5M tokens already sold (with 9 decimals)
            &pricing_config,
        );
        
        
        // Should span two tiers:
        // Tier 0: 0.5M tokens left at $0.025 = $12,500  
        // Tier 1: $7,500 at $0.0325 = ~230,769 tokens
        // Total: ~730,769 tokens
        assert!(tokens > Uint128::from(700_000_000_000_000u128)); // > 700k tokens (9 decimals)  
        assert!(tokens < Uint128::from(800_000_000_000_000u128)); // < 800k tokens (9 decimals)
        assert_eq!(usd_spent, Uint128::from(20_000_000_000u128)); // Full $20,000 spent
        assert_eq!(start_tier, 0); // Started in tier 0
        assert_eq!(end_tier, 1); // Ended in tier 1
        // Average price should be between $0.025 and $0.0325
        assert!(avg_price > Uint128::from(25000u128)); // > $0.025
        assert!(avg_price < Uint128::from(32500u128)); // < $0.0325
    }
} 