//! reward-splitter — minimal push-only immutable payment splitter for
//! redistributing mined epoch rewards on the Gonka network.
//!
//! GNK (`ngonka`) sent to the contract address is divided among a fixed set
//! of payees proportionally to fixed shares when anyone calls `Distribute`.
//! No admin, no owner, no update messages, no migrate entry point: combined
//! with instantiating with `admin: None` (wasm level, `--no-admin` in the
//! CLI), an instance is immutable forever. Instances are cheap and
//! disposable — one `code_id`, a fresh instance per epoch / payee set.
//!
//! Naming follows the PaymentSplitter canon: `payees` hold `shares` of a
//! `split`; each payee's cut of every distribution is
//! `shares / total_shares`.
//!
//! Single-file contract: this `lib.rs` is the whole thing. It can be pasted
//! as-is into the Gonka Contract Playground, or built locally:
//!
//! ```text
//! cargo build --release --target wasm32-unknown-unknown   # Rust <= 1.81
//! wasm-opt -Os --signext-lowering <raw>.wasm -o reward_splitter.wasm
//! ```
//!
//! Semantics:
//! - `Distribute {}` is permissionless and idempotent: funds can only ever
//!   move to the fixed payees, so a caller gains nothing beyond paying gas
//!   (currently zero on Gonka — a cron can crank it for free). Funds may be
//!   attached to the call and are distributed in the same transaction. When
//!   there is nothing to pay, the call succeeds as a no-op (`distributed:
//!   0`, no `payout` events) instead of erroring, so batched and
//!   cross-contract callers compose safely.
//! - Each payee receives `floor(balance * shares / total_shares)`. The
//!   floor-division remainder (strictly less than the payee count, in base
//!   units of ngonka) stays on the contract and is re-absorbed into the
//!   payouts as soon as new funds arrive — dust does not accumulate.
//! - Every payout is logged as a `payout` event (`wasm-payout` in tx
//!   results) with payee/denom/amount attributes.
//! - The contract handles `ngonka` ONLY, by design: rewards exist in no
//!   other denom. Any other tokens sent to the contract address are ignored
//!   and unrecoverable — do not send them.

use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::{
    entry_point, to_json_binary, Addr, BankMsg, Binary, Coin, Deps, DepsMut, Env, Event,
    MessageInfo, Response, StdError, StdResult, Uint128,
};
use cw_storage_plus::Item;
use std::collections::BTreeSet;

/// The one and only denom this splitter distributes.
pub const DENOM: &str = "ngonka";
/// Upper bound on the payee list: keeps the `Distribute` loop (one bank
/// send per payee) safely inside block gas limits, forever.
pub const MAX_PAYEES: usize = 120;

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

#[cw_serde]
pub struct Payee {
    pub address: String,
    /// Shares held by this payee; their cut is `shares / total_shares`.
    /// Must be > 0.
    pub shares: u64,
}

/// The payee list is fixed here, forever. Instantiate with `admin: None`
/// to make the instance fully immutable. Attached funds are simply held and
/// become distributable immediately.
#[cw_serde]
pub struct InstantiateMsg {
    /// 1..=120 (`MAX_PAYEES`) unique addresses with positive shares.
    pub payees: Vec<Payee>,
}

#[cw_serde]
pub enum ExecuteMsg {
    /// Split the contract's current `ngonka` balance among the payees.
    Distribute {},
}

#[cw_serde]
#[derive(QueryResponses)]
pub enum QueryMsg {
    /// The immutable split definition: payees and total shares.
    #[returns(SplitResponse)]
    Split {},
}

#[cw_serde]
pub struct SplitResponse {
    pub payees: Vec<Payee>,
    pub total_shares: u64,
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

/// The immutable split definition, written once at instantiation.
///
/// `total_shares` is deliberately NOT materialized: it is derivable, the
/// payee list is loaded wholesale on every access anyway (summing <= 120
/// u64s on top is negligible), and a value that does not exist can never go
/// out of sync — even in a fork that adds mutability.
#[cw_serde]
pub struct Split {
    pub payees: Vec<(Addr, u64)>,
}

pub const SPLIT: Item<Split> = Item::new("split");

/// Sum of all shares. Cannot overflow: the sum was `checked_add`-validated
/// at instantiation and the payee list is immutable ever after.
fn sum_shares(payees: &[(Addr, u64)]) -> u64 {
    payees.iter().map(|(_, shares)| shares).sum()
}

// ---------------------------------------------------------------------------
// Entry points
// ---------------------------------------------------------------------------

#[entry_point]
pub fn instantiate(
    deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    msg: InstantiateMsg,
) -> StdResult<Response> {
    if msg.payees.is_empty() {
        return Err(StdError::generic_err("payees list must not be empty"));
    }
    if msg.payees.len() > MAX_PAYEES {
        return Err(StdError::generic_err(format!(
            "too many payees: {} (max {MAX_PAYEES})",
            msg.payees.len()
        )));
    }

    let mut seen: BTreeSet<Addr> = BTreeSet::new();
    let mut total_shares: u64 = 0;
    let mut payees: Vec<(Addr, u64)> = Vec::with_capacity(msg.payees.len());
    for p in msg.payees {
        let addr = deps.api.addr_validate(&p.address)?;
        if !seen.insert(addr.clone()) {
            return Err(StdError::generic_err(format!(
                "duplicate payee address: {addr}"
            )));
        }
        if p.shares == 0 {
            return Err(StdError::generic_err(format!(
                "payee {addr} has zero shares"
            )));
        }
        total_shares = total_shares
            .checked_add(p.shares)
            .ok_or_else(|| StdError::generic_err("sum of payee shares overflows u64"))?;
        payees.push((addr, p.shares));
    }

    let count = payees.len();
    SPLIT.save(deps.storage, &Split { payees })?;
    Ok(Response::new()
        .add_attribute("action", "instantiate")
        .add_attribute("payees", count.to_string())
        .add_attribute("total_shares", total_shares.to_string()))
}

#[entry_point]
pub fn execute(deps: DepsMut, env: Env, info: MessageInfo, msg: ExecuteMsg) -> StdResult<Response> {
    match msg {
        ExecuteMsg::Distribute {} => distribute(deps, env, info),
    }
}

fn distribute(deps: DepsMut, env: Env, info: MessageInfo) -> StdResult<Response> {
    let split = SPLIT.load(deps.storage)?;
    let total_shares = sum_shares(&split.payees);
    let balance = deps
        .querier
        .query_balance(&env.contract.address, DENOM)?
        .amount;

    let mut msgs: Vec<BankMsg> = Vec::with_capacity(split.payees.len());
    let mut events: Vec<Event> = Vec::with_capacity(split.payees.len());
    let mut paid = Uint128::zero();

    for (addr, shares) in &split.payees {
        // Floored payout; sum over payees <= balance, remainder stays on the
        // contract until the next distribution.
        let payout = balance.multiply_ratio(*shares, total_shares);
        if payout.is_zero() {
            continue;
        }
        paid += payout;
        msgs.push(BankMsg::Send {
            to_address: addr.to_string(),
            amount: vec![Coin {
                denom: DENOM.to_string(),
                amount: payout,
            }],
        });
        events.push(
            Event::new("payout")
                .add_attribute("payee", addr.as_str())
                .add_attribute("denom", DENOM)
                .add_attribute("amount", payout.to_string()),
        );
    }

    // Deliberately NOT an error when there is nothing to pay (empty balance,
    // or balance < total_shares so every floored payout is zero — dust waits
    // for the next deposit): Distribute is an idempotent "ensure
    // distributed" crank, and it writes no state, so an error would protect
    // nothing. A no-op success composes safely when batched with other
    // messages or called from another contract — in Cosmos a single failing
    // message reverts the whole transaction. Observers distinguish a no-op
    // by `distributed: 0` and the absence of `payout` events.
    Ok(Response::new()
        .add_attribute("action", "distribute")
        .add_attribute("sender", info.sender)
        .add_attribute("distributed", paid.to_string())
        .add_messages(msgs)
        .add_events(events))
}

#[entry_point]
pub fn query(deps: Deps, _env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::Split {} => {
            let split = SPLIT.load(deps.storage)?;
            let total_shares = sum_shares(&split.payees);
            to_json_binary(&SplitResponse {
                payees: split
                    .payees
                    .into_iter()
                    .map(|(addr, shares)| Payee {
                        address: addr.into_string(),
                        shares,
                    })
                    .collect(),
                total_shares,
            })
        }
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use cosmwasm_std::{coin, coins};
    use cw_multi_test::{App, ContractWrapper, Executor};

    fn setup(shares: &[u64]) -> (App, Addr, Addr, Vec<Addr>) {
        let mut app = App::new(|router, api, storage| {
            let payer = api.addr_make("payer");
            router
                .bank
                .init_balance(
                    storage,
                    &payer,
                    vec![coin(1_000_000, DENOM), coin(1_000_000, "uatom")],
                )
                .unwrap();
        });
        let payer = app.api().addr_make("payer");
        let payees: Vec<Addr> = (0..shares.len())
            .map(|i| app.api().addr_make(&format!("p{i}")))
            .collect();
        let code_id = app.store_code(Box::new(ContractWrapper::new(execute, instantiate, query)));
        let msg = InstantiateMsg {
            payees: payees
                .iter()
                .zip(shares)
                .map(|(a, s)| Payee {
                    address: a.to_string(),
                    shares: *s,
                })
                .collect(),
        };
        let contract = app
            .instantiate_contract(code_id, payer.clone(), &msg, &[], "mini", None)
            .unwrap();
        (app, contract, payer, payees)
    }

    fn bal(app: &App, addr: &Addr, denom: &str) -> u128 {
        app.wrap().query_balance(addr, denom).unwrap().amount.u128()
    }

    fn distributed_attr(res: &cw_multi_test::AppResponse) -> String {
        res.events
            .iter()
            .find(|e| e.ty == "wasm")
            .and_then(|e| e.attributes.iter().find(|a| a.key == "distributed"))
            .map(|a| a.value.clone())
            .unwrap()
    }

    fn payout_events(res: &cw_multi_test::AppResponse) -> usize {
        res.events.iter().filter(|e| e.ty == "wasm-payout").count()
    }

    #[test]
    fn splits_proportionally_and_dust_is_reabsorbed() {
        let (mut app, contract, payer, payees) = setup(&[1, 1, 1]);
        app.send_tokens(payer.clone(), contract.clone(), &coins(100, DENOM))
            .unwrap();
        app.execute_contract(payer.clone(), contract.clone(), &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        for p in &payees {
            assert_eq!(bal(&app, p, DENOM), 33);
        }
        assert_eq!(bal(&app, &contract, DENOM), 1); // dust < payee count

        // dust + new deposit: balance 3 -> +1 each, dust gone
        app.send_tokens(payer.clone(), contract.clone(), &coins(2, DENOM))
            .unwrap();
        app.execute_contract(payer, contract.clone(), &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        for p in &payees {
            assert_eq!(bal(&app, p, DENOM), 34);
        }
        assert_eq!(bal(&app, &contract, DENOM), 0);
    }

    #[test]
    fn permissionless_crank_and_repeat_is_noop() {
        let (mut app, contract, payer, payees) = setup(&[7, 3]);
        app.send_tokens(payer, contract.clone(), &coins(1_000, DENOM))
            .unwrap();
        let stranger = app.api().addr_make("stranger");
        app.execute_contract(stranger.clone(), contract.clone(), &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        assert_eq!(bal(&app, &payees[0], DENOM), 700);
        assert_eq!(bal(&app, &payees[1], DENOM), 300);

        // Nothing left: the repeat crank succeeds as an explicit no-op.
        let res = app
            .execute_contract(stranger, contract, &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        assert_eq!(distributed_attr(&res), "0");
        assert_eq!(payout_events(&res), 0);
        assert_eq!(bal(&app, &payees[0], DENOM), 700);
        assert_eq!(bal(&app, &payees[1], DENOM), 300);
    }

    #[test]
    fn emits_payout_events() {
        let (mut app, contract, payer, payees) = setup(&[7, 3]);
        app.send_tokens(payer.clone(), contract.clone(), &coins(1_000, DENOM))
            .unwrap();
        let res = app
            .execute_contract(payer, contract, &ExecuteMsg::Distribute {}, &[])
            .unwrap();

        // Custom contract events are surfaced with the `wasm-` prefix.
        let payouts: Vec<&Event> = res
            .events
            .iter()
            .filter(|e| e.ty == "wasm-payout")
            .collect();
        assert_eq!(payouts.len(), 2);
        let attr = |e: &Event, k: &str| {
            e.attributes
                .iter()
                .find(|a| a.key == k)
                .map(|a| a.value.clone())
                .unwrap()
        };
        assert_eq!(attr(payouts[0], "payee"), payees[0].to_string());
        assert_eq!(attr(payouts[0], "denom"), DENOM);
        assert_eq!(attr(payouts[0], "amount"), "700");
        assert_eq!(attr(payouts[1], "payee"), payees[1].to_string());
        assert_eq!(attr(payouts[1], "amount"), "300");
    }

    #[test]
    fn foreign_denoms_are_ignored() {
        let (mut app, contract, payer, payees) = setup(&[1]);
        app.send_tokens(payer.clone(), contract.clone(), &coins(100, DENOM))
            .unwrap();
        app.send_tokens(payer.clone(), contract.clone(), &coins(40, "uatom"))
            .unwrap();

        app.execute_contract(payer.clone(), contract.clone(), &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        assert_eq!(bal(&app, &payees[0], DENOM), 100);
        assert_eq!(bal(&app, &payees[0], "uatom"), 0);
        // foreign tokens stay on the contract, invisible to Distribute
        assert_eq!(bal(&app, &contract, "uatom"), 40);
        let res = app
            .execute_contract(payer, contract, &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        assert_eq!(distributed_attr(&res), "0");
    }

    #[test]
    fn dust_only_balance_is_noop_until_topped_up() {
        let (mut app, contract, payer, payees) = setup(&[7, 3]);
        // balance 1 < total_shares 10: every floored payout is zero
        app.send_tokens(payer.clone(), contract.clone(), &coins(1, DENOM))
            .unwrap();
        let res = app
            .execute_contract(payer.clone(), contract.clone(), &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        assert_eq!(distributed_attr(&res), "0");
        assert_eq!(payout_events(&res), 0);
        assert_eq!(bal(&app, &contract, DENOM), 1);

        app.send_tokens(payer.clone(), contract.clone(), &coins(9, DENOM))
            .unwrap();
        app.execute_contract(payer, contract, &ExecuteMsg::Distribute {}, &[])
            .unwrap();
        assert_eq!(bal(&app, &payees[0], DENOM), 7);
        assert_eq!(bal(&app, &payees[1], DENOM), 3);
    }

    #[test]
    fn split_query_reports_computed_total() {
        let (app, contract, _payer, payees) = setup(&[7, 3]);
        let resp: SplitResponse = app
            .wrap()
            .query_wasm_smart(&contract, &QueryMsg::Split {})
            .unwrap();
        assert_eq!(resp.total_shares, 10);
        assert_eq!(resp.payees.len(), 2);
        assert_eq!(resp.payees[0].address, payees[0].to_string());
        assert_eq!(resp.payees[0].shares, 7);
    }

    #[test]
    fn instantiate_validation() {
        let mut app = App::default();
        let creator = app.api().addr_make("creator");
        let a = app.api().addr_make("a");
        let code_id = app.store_code(Box::new(ContractWrapper::new(execute, instantiate, query)));
        for (payees, needle) in [
            (vec![], "must not be empty"),
            (
                vec![
                    Payee {
                        address: a.to_string(),
                        shares: 1,
                    },
                    Payee {
                        address: a.to_string(),
                        shares: 2,
                    },
                ],
                "duplicate payee",
            ),
            (
                vec![Payee {
                    address: a.to_string(),
                    shares: 0,
                }],
                "zero shares",
            ),
        ] {
            let err = app
                .instantiate_contract(
                    code_id,
                    creator.clone(),
                    &InstantiateMsg { payees },
                    &[],
                    "x",
                    None,
                )
                .unwrap_err();
            assert!(err.root_cause().to_string().contains(needle));
        }
    }

    #[test]
    fn more_than_max_payees_rejected() {
        let mut app = App::default();
        let creator = app.api().addr_make("creator");
        let code_id = app.store_code(Box::new(ContractWrapper::new(execute, instantiate, query)));
        let payees: Vec<Payee> = (0..=MAX_PAYEES)
            .map(|i| Payee {
                address: app.api().addr_make(&format!("p{i}")).to_string(),
                shares: 1,
            })
            .collect();
        assert_eq!(payees.len(), MAX_PAYEES + 1);
        let err = app
            .instantiate_contract(code_id, creator, &InstantiateMsg { payees }, &[], "x", None)
            .unwrap_err();
        assert!(err.root_cause().to_string().contains("too many payees"));
    }

    #[test]
    fn funds_attached_to_distribute_are_included() {
        let (mut app, contract, payer, payees) = setup(&[1]);
        // no prior balance: attach 500 to the call itself
        app.execute_contract(
            payer,
            contract.clone(),
            &ExecuteMsg::Distribute {},
            &coins(500, DENOM),
        )
        .unwrap();
        assert_eq!(bal(&app, &payees[0], DENOM), 500);
        assert_eq!(bal(&app, &contract, DENOM), 0);
    }
}

