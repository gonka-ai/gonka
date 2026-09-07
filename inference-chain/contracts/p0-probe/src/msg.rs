use cosmwasm_schema::{cw_serde, QueryResponses};
use cosmwasm_std::Binary;

#[cw_serde]
pub struct InstantiateMsg {}

#[cw_serde]
#[derive(QueryResponses)]
pub enum QueryMsg {
    /// Exercises `/inference.inference.Query/GetCurrentEpoch` from Wasm.
    #[returns(CurrentEpochResponse)]
    GetCurrentEpoch {},
    /// Exercises `/inference.inference.Query/ListClaimRecipients` from Wasm.
    #[returns(ClaimRecipientsResponse)]
    ListClaimRecipients { participant: String },
    /// Exercises the participant-scoped performance-summary query from Wasm.
    #[returns(PerformanceSummaryResponse)]
    EpochPerformanceSummary {
        epoch_index: u64,
        participant_id: String,
    },
    /// Exercises `/inference.streamvesting.Query/TotalVestingAmount` from Wasm.
    #[returns(TotalVestingResponse)]
    TotalVesting { participant_address: String },
    /// Test-only escape hatch for asserting denied paths and malformed payloads.
    /// A production contract must not expose arbitrary gRPC queries.
    #[returns(RawGrpcResponse)]
    RawGrpc { path: String, data: Binary },
}

#[cw_serde]
pub struct CurrentEpochResponse {
    pub epoch: u64,
}

#[cw_serde]
pub struct ClaimRecipientResponse {
    pub epoch: u64,
    pub recipient: String,
}

#[cw_serde]
pub struct ClaimRecipientsResponse {
    pub entries: Vec<ClaimRecipientResponse>,
}

#[cw_serde]
pub struct PerformanceSummaryResponse {
    pub epoch_index: u64,
    pub participant_id: String,
    pub earned_coins: u64,
    pub rewarded_coins: u64,
    pub claimed: bool,
}

#[cw_serde]
pub struct CoinResponse {
    pub denom: String,
    pub amount: String,
}

#[cw_serde]
pub struct TotalVestingResponse {
    pub total_amount: Vec<CoinResponse>,
}

#[cw_serde]
pub struct RawGrpcResponse {
    pub data: Binary,
}
