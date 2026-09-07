//! Minimal handwritten protobuf wire mirror for the allowlist test fixture.
//!
//! Canonical schemas at Gonka commit `379bebced638aeb5e6077bfd51c986f898443832`:
//! - `proto/inference/inference/query.proto`
//! - `proto/inference/inference/claim_recipient.proto`
//! - `proto/inference/inference/epoch_performance_summary.proto`
//! - `proto/inference/streamvesting/query.proto`
//!
//! Only fields consumed by the diagnostic contract are represented. Unknown
//! protobuf fields are intentionally ignored. The full-app Go/Wasm regression
//! test is the compatibility guard against changes in the canonical schemas.

use prost::Message;

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryGetCurrentEpochRequest {}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryGetCurrentEpochResponse {
    #[prost(uint64, tag = "1")]
    pub epoch: u64,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryListClaimRecipientsRequest {
    #[prost(string, tag = "1")]
    pub participant: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct ClaimRecipientEntry {
    #[prost(uint64, tag = "1")]
    pub epoch: u64,
    #[prost(string, tag = "2")]
    pub recipient: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryListClaimRecipientsResponse {
    #[prost(message, repeated, tag = "1")]
    pub entries: Vec<ClaimRecipientEntry>,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryEpochPerformanceSummaryByParticipantRequest {
    #[prost(uint64, tag = "1")]
    pub epoch_index: u64,
    #[prost(string, tag = "2")]
    pub participant_id: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct EpochPerformanceSummary {
    #[prost(uint64, tag = "1")]
    pub epoch_index: u64,
    #[prost(string, tag = "2")]
    pub participant_id: String,
    #[prost(uint64, tag = "5")]
    pub earned_coins: u64,
    #[prost(uint64, tag = "6")]
    pub rewarded_coins: u64,
    #[prost(bool, tag = "10")]
    pub claimed: bool,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryEpochPerformanceSummaryByParticipantResponse {
    #[prost(message, optional, tag = "1")]
    pub epoch_performance_summary: Option<EpochPerformanceSummary>,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryTotalVestingAmountRequest {
    #[prost(string, tag = "1")]
    pub participant_address: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct ProtoCoin {
    #[prost(string, tag = "1")]
    pub denom: String,
    #[prost(string, tag = "2")]
    pub amount: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct QueryTotalVestingAmountResponse {
    #[prost(message, repeated, tag = "1")]
    pub total_amount: Vec<ProtoCoin>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn participant_summary_request_uses_canonical_field_tags() {
        let request = QueryEpochPerformanceSummaryByParticipantRequest {
            epoch_index: 20,
            participant_id: "host".to_string(),
        };

        assert_eq!(
            request.encode_to_vec(),
            vec![0x08, 0x14, 0x12, 0x04, b'h', b'o', b's', b't']
        );
    }

    #[test]
    fn repeated_entries_and_coins_round_trip() {
        let recipients = QueryListClaimRecipientsResponse {
            entries: vec![ClaimRecipientEntry {
                epoch: 20,
                recipient: "deal".to_string(),
            }],
        };
        assert_eq!(
            QueryListClaimRecipientsResponse::decode(recipients.encode_to_vec().as_slice())
                .unwrap(),
            recipients
        );

        let vesting = QueryTotalVestingAmountResponse {
            total_amount: vec![ProtoCoin {
                denom: "ngonka".to_string(),
                amount: "700000000".to_string(),
            }],
        };
        assert_eq!(
            QueryTotalVestingAmountResponse::decode(vesting.encode_to_vec().as_slice()).unwrap(),
            vesting
        );
    }
}
