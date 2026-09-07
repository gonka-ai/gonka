use cosmwasm_std::{
    entry_point, to_json_binary, Binary, ContractResult, Deps, DepsMut, Env, GrpcQuery,
    MessageInfo, QueryRequest, Response, StdError, StdResult, SystemResult,
};
use prost::Message;

use crate::msg::{
    ClaimRecipientResponse, ClaimRecipientsResponse, CoinResponse, CurrentEpochResponse,
    InstantiateMsg, PerformanceSummaryResponse, QueryMsg, RawGrpcResponse, TotalVestingResponse,
};
use crate::proto::{
    QueryEpochPerformanceSummaryByParticipantRequest,
    QueryEpochPerformanceSummaryByParticipantResponse, QueryGetCurrentEpochRequest,
    QueryGetCurrentEpochResponse, QueryListClaimRecipientsRequest,
    QueryListClaimRecipientsResponse, QueryTotalVestingAmountRequest,
    QueryTotalVestingAmountResponse,
};

const GET_CURRENT_EPOCH_PATH: &str = "/inference.inference.Query/GetCurrentEpoch";
const LIST_CLAIM_RECIPIENTS_PATH: &str = "/inference.inference.Query/ListClaimRecipients";
const PERFORMANCE_SUMMARY_PATH: &str =
    "/inference.inference.Query/EpochPerformanceSummaryByParticipant";
const TOTAL_VESTING_PATH: &str = "/inference.streamvesting.Query/TotalVestingAmount";

#[entry_point]
pub fn instantiate(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _msg: InstantiateMsg,
) -> StdResult<Response> {
    Ok(Response::new().add_attribute("action", "instantiate_wasm_grpc_query_probe"))
}

#[entry_point]
pub fn query(deps: Deps, _env: Env, msg: QueryMsg) -> StdResult<Binary> {
    match msg {
        QueryMsg::GetCurrentEpoch {} => {
            let response: QueryGetCurrentEpochResponse = query_proto(
                deps,
                GET_CURRENT_EPOCH_PATH,
                &QueryGetCurrentEpochRequest {},
            )?;
            to_json_binary(&CurrentEpochResponse {
                epoch: response.epoch,
            })
        }
        QueryMsg::ListClaimRecipients { participant } => {
            let response: QueryListClaimRecipientsResponse = query_proto(
                deps,
                LIST_CLAIM_RECIPIENTS_PATH,
                &QueryListClaimRecipientsRequest { participant },
            )?;
            to_json_binary(&ClaimRecipientsResponse {
                entries: response
                    .entries
                    .into_iter()
                    .map(|entry| ClaimRecipientResponse {
                        epoch: entry.epoch,
                        recipient: entry.recipient,
                    })
                    .collect(),
            })
        }
        QueryMsg::EpochPerformanceSummary {
            epoch_index,
            participant_id,
        } => {
            let response: QueryEpochPerformanceSummaryByParticipantResponse = query_proto(
                deps,
                PERFORMANCE_SUMMARY_PATH,
                &QueryEpochPerformanceSummaryByParticipantRequest {
                    epoch_index,
                    participant_id,
                },
            )?;
            let summary = response
                .epoch_performance_summary
                .ok_or_else(|| StdError::msg("missing epoch performance summary"))?;
            to_json_binary(&PerformanceSummaryResponse {
                epoch_index: summary.epoch_index,
                participant_id: summary.participant_id,
                earned_coins: summary.earned_coins,
                rewarded_coins: summary.rewarded_coins,
                claimed: summary.claimed,
            })
        }
        QueryMsg::TotalVesting {
            participant_address,
        } => {
            let response: QueryTotalVestingAmountResponse = query_proto(
                deps,
                TOTAL_VESTING_PATH,
                &QueryTotalVestingAmountRequest {
                    participant_address,
                },
            )?;
            to_json_binary(&TotalVestingResponse {
                total_amount: response
                    .total_amount
                    .into_iter()
                    .map(|coin| CoinResponse {
                        denom: coin.denom,
                        amount: coin.amount,
                    })
                    .collect(),
            })
        }
        QueryMsg::RawGrpc { path, data } => {
            let data = query_grpc(deps, &path, data)?;
            to_json_binary(&RawGrpcResponse { data })
        }
    }
}

fn query_grpc(deps: Deps, path: &str, data: Binary) -> StdResult<Binary> {
    let request: QueryRequest<cosmwasm_std::Empty> = QueryRequest::Grpc(GrpcQuery {
        path: path.to_string(),
        data,
    });
    let raw = cosmwasm_std::to_json_vec(&request)
        .map_err(|error| StdError::msg(format!("serialize gRPC query: {error}")))?;
    match deps.querier.raw_query(&raw) {
        SystemResult::Err(error) => Err(StdError::msg(format!("system query error: {error}"))),
        SystemResult::Ok(ContractResult::Err(error)) => {
            Err(StdError::msg(format!("contract query error: {error}")))
        }
        SystemResult::Ok(ContractResult::Ok(value)) => Ok(value),
    }
}

fn query_proto<TRequest, TResponse>(
    deps: Deps,
    path: &str,
    request: &TRequest,
) -> StdResult<TResponse>
where
    TRequest: Message,
    TResponse: Message + Default,
{
    let response = query_grpc(deps, path, Binary::from(request.encode_to_vec()))?;
    TResponse::decode(response.as_slice())
        .map_err(|error| StdError::msg(format!("decode protobuf response: {error}")))
}
