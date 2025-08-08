use cosmwasm_std::{
    entry_point, to_json_binary, Addr, Binary, Deps, DepsMut, Env, MessageInfo, Order, Response,
    StdResult, Uint128,
};
use cw2::set_contract_version;
use cw_storage_plus::{Bound, Item};

use crate::error::ContractError;
use crate::msg::{
    AllAccountsResponse, AllAllowancesResponse, AllowanceInfo, AllowanceResponse, BalanceResponse,
    BridgeInfoResponse, DownloadLogoResponse, EmbeddedLogo, ExecuteMsg, Expiration,
    InstantiateMsg, LogoInfo, MarketingInfoResponse, MinterResponse, QueryMsg, TokenInfoResponse,
};
use crate::state::{
    BridgeInfo, MarketingInfo, TokenInfo,
    ALLOWANCES, BALANCES, BRIDGE_INFO, LOGO, MARKETING_INFO, TOKEN_INFO,
};

// Admin storage: stores the address of the contract admin (instantiator)
pub const ADMIN: Item<Addr> = Item::new("admin");

const CONTRACT_NAME: &str = "wrapped-token";
const CONTRACT_VERSION: &str = env!("CARGO_PKG_VERSION");

// Default pagination limit
const DEFAULT_LIMIT: u32 = 10;
const MAX_LIMIT: u32 = 30;

#[entry_point]
pub fn instantiate(
    deps: DepsMut,
    _env: Env,
    info: MessageInfo,
    msg: InstantiateMsg,
) -> Result<Response, ContractError> {
    set_contract_version(deps.storage, CONTRACT_NAME, CONTRACT_VERSION)?;

    // Store the contract admin (instantiator)
    ADMIN.save(deps.storage, &info.sender)?;

    // Use placeholder values for metadata at instantiation
    let name = "".to_string();
    let symbol = "".to_string();
    let decimals = 0u8;

    // Validate minter
    let mint = match msg.mint {
        Some(m) => Some(MinterResponse {
            minter: deps.api.addr_validate(&m.minter)?.to_string(),
            cap: m.cap,
        }),
        None => None,
    };

    // Calculate total supply from initial balances
    let total_supply: Uint128 = msg
        .initial_balances
        .iter()
        .map(|coin| coin.amount)
        .sum();

    // Store token info with placeholder metadata
    let token_info = TokenInfo {
        name: name.clone(),
        symbol: symbol.clone(),
        decimals,
        total_supply,
        mint: mint.clone(),
    };
    TOKEN_INFO.save(deps.storage, &token_info)?;

    // Store bridge-specific information
    let bridge_info = BridgeInfo {
        chain_id: msg.chain_id,
        contract_address: msg.contract_address,
    };
    BRIDGE_INFO.save(deps.storage, &bridge_info)?;

    // Set initial balances
    for balance in msg.initial_balances {
        let addr = deps.api.addr_validate(&balance.address)?;
        BALANCES.save(deps.storage, &addr, &balance.amount)?;
    }

    // Store marketing info if provided
    if let Some(marketing) = msg.marketing {
        let marketing_info = MarketingInfo {
            project: marketing.project,
            description: marketing.description,
            marketing: marketing
                .marketing
                .as_ref()
                .map(|addr| deps.api.addr_validate(addr))
                .transpose()?,
            logo: marketing.logo,
        };
        MARKETING_INFO.save(deps.storage, &marketing_info)?;
    }

    Ok(Response::new()
        .add_attribute("method", "instantiate")
        .add_attribute("name", &name)
        .add_attribute("symbol", &symbol)
        .add_attribute("decimals", decimals.to_string())
        .add_attribute("total_supply", total_supply)
        .add_attribute("bridge_chain_id", &bridge_info.chain_id)
        .add_attribute("bridge_contract", &bridge_info.contract_address))
}

// (Removed: query_token_metadata, not used anymore)

#[entry_point]
pub fn execute(
    deps: DepsMut,
    env: Env,
    info: MessageInfo,
    msg: ExecuteMsg,
) -> Result<Response, ContractError> {
    match msg {
        ExecuteMsg::Transfer { recipient, amount } => {
            transfer(deps, env, info, recipient, amount)
        }
        ExecuteMsg::Burn { amount } => burn(deps, env, info, amount),
        ExecuteMsg::Send {
            contract,
            amount,
            msg,
        } => send(deps, env, info, contract, amount, msg),
        ExecuteMsg::Mint { recipient, amount } => mint(deps, env, info, recipient, amount),
        ExecuteMsg::IncreaseAllowance {
            spender,
            amount,
            expires,
        } => increase_allowance(deps, env, info, spender, amount, expires),
        ExecuteMsg::DecreaseAllowance {
            spender,
            amount,
            expires,
        } => decrease_allowance(deps, env, info, spender, amount, expires),
        ExecuteMsg::TransferFrom {
            owner,
            recipient,
            amount,
        } => transfer_from(deps, env, info, owner, recipient, amount),
        ExecuteMsg::SendFrom {
            owner,
            contract,
            amount,
            msg,
        } => send_from(deps, env, info, owner, contract, amount, msg),
        ExecuteMsg::BurnFrom { owner, amount } => burn_from(deps, env, info, owner, amount),
        ExecuteMsg::Withdraw { amount } => withdraw(deps, env, info, amount),
        ExecuteMsg::UpdateMarketing {
            project,
            description,
            marketing,
        } => update_marketing(deps, env, info, project, description, marketing),
        ExecuteMsg::UploadLogo(logo) => upload_logo(deps, env, info, logo),
        ExecuteMsg::UpdateMetadata { name, symbol, decimals } => {
            update_metadata(deps, info, name, symbol, decimals)
        }
    }
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

    TOKEN_INFO.update(deps.storage, |mut info| -> StdResult<_> {
        info.name = name.clone();
        info.symbol = symbol.clone();
        info.decimals = decimals;
        Ok(info)
    })?;

    Ok(Response::new()
        .add_attribute("method", "update_metadata")
        .add_attribute("name", name)
        .add_attribute("symbol", symbol)
        .add_attribute("decimals", decimals.to_string()))
}

// Special bridge withdraw function that burns tokens
fn withdraw(
    deps: DepsMut,
    _env: Env,
    info: MessageInfo,
    amount: Uint128,
) -> Result<Response, ContractError> {
    // For now, this just burns tokens from the user's balance
    // TODO: When bridge query is ready, this should also trigger the bridge withdrawal
    
    if amount.is_zero() {
        return Err(ContractError::InsufficientFunds {
            balance: 0,
            required: 1,
        });
    }

    let sender_addr = info.sender;
    let balance = BALANCES.may_load(deps.storage, &sender_addr)?.unwrap_or_default();

    if balance < amount {
        return Err(ContractError::InsufficientFunds {
            balance: balance.u128(),
            required: amount.u128(),
        });
    }

    // Burn tokens by reducing balance and total supply
    let new_balance = balance - amount;
    if new_balance.is_zero() {
        BALANCES.remove(deps.storage, &sender_addr);
    } else {
        BALANCES.save(deps.storage, &sender_addr, &new_balance)?;
    }

    TOKEN_INFO.update(deps.storage, |mut info| -> StdResult<_> {
        info.total_supply = info.total_supply.checked_sub(amount)?;
        Ok(info)
    })?;

    // TODO: When bridge query endpoint is ready, add bridge withdrawal logic here
    
    Ok(Response::new()
        .add_attribute("method", "withdraw")
        .add_attribute("from", sender_addr)
        .add_attribute("amount", amount)
        .add_attribute("burned", amount)
        .add_attribute("note", "Bridge withdrawal not fully implemented - tokens burned"))
}

fn transfer(
    deps: DepsMut,
    _env: Env,
    info: MessageInfo,
    recipient: String,
    amount: Uint128,
) -> Result<Response, ContractError> {
    if amount.is_zero() {
        return Ok(Response::new());
    }

    let rcpt_addr = deps.api.addr_validate(&recipient)?;
    let sender_addr = info.sender;

    // Check sender balance
    let sender_balance = BALANCES.may_load(deps.storage, &sender_addr)?.unwrap_or_default();
    if sender_balance < amount {
        return Err(ContractError::InsufficientFunds {
            balance: sender_balance.u128(),
            required: amount.u128(),
        });
    }

    // Update sender balance
    let new_sender_balance = sender_balance - amount;
    if new_sender_balance.is_zero() {
        BALANCES.remove(deps.storage, &sender_addr);
    } else {
        BALANCES.save(deps.storage, &sender_addr, &new_sender_balance)?;
    }

    // Update recipient balance
    let rcpt_balance = BALANCES.may_load(deps.storage, &rcpt_addr)?.unwrap_or_default();
    BALANCES.save(deps.storage, &rcpt_addr, &(rcpt_balance + amount))?;

    Ok(Response::new()
        .add_attribute("method", "transfer")
        .add_attribute("from", sender_addr)
        .add_attribute("to", recipient)
        .add_attribute("amount", amount))
}

fn burn(
    deps: DepsMut,
    _env: Env,
    info: MessageInfo,
    amount: Uint128,
) -> Result<Response, ContractError> {
    if amount.is_zero() {
        return Ok(Response::new());
    }

    let sender_addr = info.sender;
    let balance = BALANCES.may_load(deps.storage, &sender_addr)?.unwrap_or_default();

    if balance < amount {
        return Err(ContractError::InsufficientFunds {
            balance: balance.u128(),
            required: amount.u128(),
        });
    }

    let new_balance = balance - amount;
    if new_balance.is_zero() {
        BALANCES.remove(deps.storage, &sender_addr);
    } else {
        BALANCES.save(deps.storage, &sender_addr, &new_balance)?;
    }

    TOKEN_INFO.update(deps.storage, |mut info| -> StdResult<_> {
        info.total_supply = info.total_supply.checked_sub(amount)?;
        Ok(info)
    })?;

    Ok(Response::new()
        .add_attribute("method", "burn")
        .add_attribute("from", sender_addr)
        .add_attribute("amount", amount))
}

fn mint(
    deps: DepsMut,
    _env: Env,
    info: MessageInfo,
    recipient: String,
    amount: Uint128,
) -> Result<Response, ContractError> {
    if amount.is_zero() {
        return Ok(Response::new());
    }

    let mut token_info = TOKEN_INFO.load(deps.storage)?;

    // Check if sender is authorized to mint
    if let Some(ref mint_info) = token_info.mint {
        if info.sender != mint_info.minter {
            return Err(ContractError::Unauthorized {});
        }

        // Check mint cap
        if let Some(cap) = mint_info.cap {
            let new_total = token_info.total_supply + amount;
            if new_total > cap {
                return Err(ContractError::CannotExceedCap {});
            }
        }
    } else {
        return Err(ContractError::Unauthorized {});
    }

    let rcpt_addr = deps.api.addr_validate(&recipient)?;

    // Update recipient balance
    let balance = BALANCES.may_load(deps.storage, &rcpt_addr)?.unwrap_or_default();
    BALANCES.save(deps.storage, &rcpt_addr, &(balance + amount))?;

    // Update total supply
    token_info.total_supply += amount;
    TOKEN_INFO.save(deps.storage, &token_info)?;

    Ok(Response::new()
        .add_attribute("method", "mint")
        .add_attribute("to", recipient)
        .add_attribute("amount", amount))
}

// Placeholder implementations for other functions
fn send(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _contract: String,
    _amount: Uint128,
    _msg: Binary,
) -> Result<Response, ContractError> {
    // TODO: Implement send functionality
    Ok(Response::new().add_attribute("method", "send"))
}

fn increase_allowance(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _spender: String,
    _amount: Uint128,
    _expires: Option<Expiration>,
) -> Result<Response, ContractError> {
    // TODO: Implement allowance functionality
    Ok(Response::new().add_attribute("method", "increase_allowance"))
}

fn decrease_allowance(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _spender: String,
    _amount: Uint128,
    _expires: Option<Expiration>,
) -> Result<Response, ContractError> {
    // TODO: Implement allowance functionality
    Ok(Response::new().add_attribute("method", "decrease_allowance"))
}

fn transfer_from(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _owner: String,
    _recipient: String,
    _amount: Uint128,
) -> Result<Response, ContractError> {
    // TODO: Implement transfer_from functionality
    Ok(Response::new().add_attribute("method", "transfer_from"))
}

fn send_from(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _owner: String,
    _contract: String,
    _amount: Uint128,
    _msg: Binary,
) -> Result<Response, ContractError> {
    // TODO: Implement send_from functionality
    Ok(Response::new().add_attribute("method", "send_from"))
}

fn burn_from(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _owner: String,
    _amount: Uint128,
) -> Result<Response, ContractError> {
    // TODO: Implement burn_from functionality
    Ok(Response::new().add_attribute("method", "burn_from"))
}

fn update_marketing(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _project: Option<String>,
    _description: Option<String>,
    _marketing: Option<String>,
) -> Result<Response, ContractError> {
    // TODO: Implement marketing update functionality
    Ok(Response::new().add_attribute("method", "update_marketing"))
}

fn upload_logo(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _logo: crate::msg::Logo,
) -> Result<Response, ContractError> {
    // TODO: Implement logo upload functionality
    Ok(Response::new().add_attribute("method", "upload_logo"))
}

#[entry_point]
pub fn query(deps: Deps, _env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::Balance { address } => to_json_binary(&query_balance(deps, address)?),
        QueryMsg::TokenInfo {} => to_json_binary(&query_token_info(deps)?),
        QueryMsg::BridgeInfo {} => to_json_binary(&query_bridge_info(deps)?),
        QueryMsg::Allowance { owner, spender } => {
            to_json_binary(&query_allowance(deps, owner, spender)?)
        }
        QueryMsg::AllAllowances {
            owner,
            start_after,
            limit,
        } => to_json_binary(&query_all_allowances(deps, owner, start_after, limit)?),
        QueryMsg::AllAccounts { start_after, limit } => {
            to_json_binary(&query_all_accounts(deps, start_after, limit)?)
        }
        QueryMsg::MarketingInfo {} => to_json_binary(&query_marketing_info(deps)?),
        QueryMsg::DownloadLogo {} => to_json_binary(&query_download_logo(deps)?),
        QueryMsg::Minter {} => to_json_binary(&query_minter(deps)?),
    }
}

fn query_balance(deps: Deps, address: String) -> StdResult<BalanceResponse> {
    let addr = deps.api.addr_validate(&address)?;
    let balance = BALANCES.may_load(deps.storage, &addr)?.unwrap_or_default();
    Ok(BalanceResponse { balance })
}

fn query_token_info(deps: Deps) -> StdResult<TokenInfoResponse> {
    let info = TOKEN_INFO.load(deps.storage)?;
    Ok(TokenInfoResponse {
        name: info.name,
        symbol: info.symbol,
        decimals: info.decimals,
        total_supply: info.total_supply,
    })
}

fn query_bridge_info(deps: Deps) -> StdResult<BridgeInfoResponse> {
    let info = BRIDGE_INFO.load(deps.storage)?;
    Ok(BridgeInfoResponse {
        chain_id: info.chain_id,
        contract_address: info.contract_address,
    })
}

fn query_allowance(deps: Deps, owner: String, spender: String) -> StdResult<AllowanceResponse> {
    let owner_addr = deps.api.addr_validate(&owner)?;
    let spender_addr = deps.api.addr_validate(&spender)?;
    let allowance = ALLOWANCES
        .may_load(deps.storage, (&owner_addr, &spender_addr))?
        .unwrap_or(crate::state::AllowanceResponse {
            allowance: Uint128::zero(),
            expires: Expiration::Never {},
        });
    Ok(AllowanceResponse {
        allowance: allowance.allowance,
        expires: allowance.expires,
    })
}

fn query_all_allowances(
    deps: Deps,
    owner: String,
    start_after: Option<String>,
    limit: Option<u32>,
) -> StdResult<AllAllowancesResponse> {
    let owner_addr = deps.api.addr_validate(&owner)?;
    let limit = limit.unwrap_or(DEFAULT_LIMIT).min(MAX_LIMIT) as usize;

    let start = start_after
        .as_ref()
        .map(|addr| deps.api.addr_validate(addr))
        .transpose()?;

    let allowances: StdResult<Vec<_>> = ALLOWANCES
        .prefix(&owner_addr)
        .range(
            deps.storage,
            start.as_ref().map(Bound::exclusive),
            None,
            Order::Ascending,
        )
        .take(limit)
        .map(|item| {
            let (spender, allowance) = item?;
            Ok(AllowanceInfo {
                spender: spender.to_string(),
                allowance: allowance.allowance,
                expires: allowance.expires,
            })
        })
        .collect();

    Ok(AllAllowancesResponse {
        allowances: allowances?,
    })
}

fn query_all_accounts(
    deps: Deps,
    start_after: Option<String>,
    limit: Option<u32>,
) -> StdResult<AllAccountsResponse> {
    let limit = limit.unwrap_or(DEFAULT_LIMIT).min(MAX_LIMIT) as usize;

    let start = start_after
        .as_ref()
        .map(|addr| deps.api.addr_validate(addr))
        .transpose()?;

    let accounts: StdResult<Vec<_>> = BALANCES
        .range(
            deps.storage,
            start.as_ref().map(Bound::exclusive),
            None,
            Order::Ascending,
        )
        .take(limit)
        .map(|item| {
            let (addr, _) = item?;
            Ok(addr.to_string())
        })
        .collect();

    Ok(AllAccountsResponse {
        accounts: accounts?,
    })
}

fn query_marketing_info(deps: Deps) -> StdResult<MarketingInfoResponse> {
    let marketing = MARKETING_INFO.may_load(deps.storage)?;
    match marketing {
        Some(info) => Ok(MarketingInfoResponse {
            project: info.project,
            description: info.description,
            marketing: info.marketing.map(|addr| addr.to_string()),
            logo: info.logo.map(|_| LogoInfo::Embedded),
        }),
        None => Ok(MarketingInfoResponse {
            project: None,
            description: None,
            marketing: None,
            logo: None,
        }),
    }
}

fn query_download_logo(deps: Deps) -> StdResult<DownloadLogoResponse> {
    let logo = LOGO.may_load(deps.storage)?;
    match logo {
        Some(crate::msg::Logo::Embedded(logo)) => match logo {
            EmbeddedLogo::Svg(data) => Ok(DownloadLogoResponse {
                mime_type: "image/svg+xml".to_string(),
                data,
            }),
            EmbeddedLogo::Png(data) => Ok(DownloadLogoResponse {
                mime_type: "image/png".to_string(),
                data,
            }),
        },
        _ => Err(cosmwasm_std::StdError::not_found("No embedded logo found")),
    }
}

fn query_minter(deps: Deps) -> StdResult<MinterResponse> {
    let token_info = TOKEN_INFO.load(deps.storage)?;
    match token_info.mint {
        Some(minter) => Ok(minter),
        None => Err(cosmwasm_std::StdError::not_found("No minter set")),
    }
}