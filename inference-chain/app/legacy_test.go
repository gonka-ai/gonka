package app

import (
	"testing"

	"github.com/cosmos/gogoproto/proto"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	streamvestingtypes "github.com/productscience/inference/x/streamvesting/types"
	"github.com/stretchr/testify/require"
)

func TestAcceptedGrpcQueries(t *testing.T) {
	queries := AcceptedGrpcQueries()

	// Keep the allowlist closed: three existing routes plus four marketplace routes.
	require.Len(t, queries, 7, "AcceptedGrpcQueries must contain exactly 7 methods")

	expectedAllowed := map[string]proto.Message{
		"/inference.inference.Query/GetCurrentEpoch":                      &inferencetypes.QueryGetCurrentEpochResponse{},
		"/inference.inference.Query/ListClaimRecipients":                  &inferencetypes.QueryListClaimRecipientsResponse{},
		"/inference.inference.Query/EpochPerformanceSummaryByParticipant": &inferencetypes.QueryEpochPerformanceSummaryByParticipantResponse{},
		"/inference.streamvesting.Query/TotalVestingAmount":               &streamvestingtypes.QueryTotalVestingAmountResponse{},
		// Existing legacy methods
		"/inference.inference.Query/ApprovedTokensForTrade":       &inferencetypes.QueryApprovedTokensForTradeResponse{},
		"/inference.inference.Query/ValidateWrappedTokenForTrade": &inferencetypes.QueryValidateWrappedTokenForTradeResponse{},
		"/inference.inference.Query/ValidateIbcTokenForTrade":     &inferencetypes.QueryValidateIbcTokenForTradeResponse{},
	}

	for path, expectedProto := range expectedAllowed {
		constructor, exists := queries[path]
		require.True(t, exists, "gRPC path %s must be allowed", path)
		require.NotNil(t, constructor, "constructor for %s must not be nil", path)
		msg := constructor()
		require.IsType(t, expectedProto, msg, "constructor for %s returned unexpected proto type", path)
	}

	// Broad scans and unrelated queries must remain rejected.
	deniedPaths := []string{
		"/inference.inference.Query/EpochPerformanceSummary",
		"/inference.inference.Query/EpochPerformanceSummaryAll",
		"/inference.inference.Query/AllParticipantCurrentStats",
		"/inference.inference.Query/AllParticipants",
		"/inference.streamvesting.Query/Params",
		"/inference.streamvesting.Query/VestingSchedule",
		"/cosmos.bank.v1beta1.Query/AllBalances",
	}

	for _, path := range deniedPaths {
		_, exists := queries[path]
		require.False(t, exists, "gRPC path %s must NOT be allowed in AcceptedGrpcQueries", path)
	}
}

func TestAcceptedStargateQueriesUnchanged(t *testing.T) {
	stargateQueries := AcceptedStargateQueries()

	// The marketplace routes use the gRPC allowlist, not the legacy Stargate one.
	require.Len(t, stargateQueries, 3, "AcceptedStargateQueries must remain exactly 3 methods")

	expectedStargate := []string{
		"/inference.inference.Query/ApprovedTokensForTrade",
		"/inference.inference.Query/ValidateWrappedTokenForTrade",
		"/inference.inference.Query/ValidateIbcTokenForTrade",
	}

	for _, path := range expectedStargate {
		_, exists := stargateQueries[path]
		require.True(t, exists, "Stargate path %s must be present", path)
	}

	marketplacePaths := []string{
		"/inference.inference.Query/GetCurrentEpoch",
		"/inference.inference.Query/ListClaimRecipients",
		"/inference.inference.Query/EpochPerformanceSummaryByParticipant",
		"/inference.streamvesting.Query/TotalVestingAmount",
	}

	for _, path := range marketplacePaths {
		_, exists := stargateQueries[path]
		require.False(t, exists, "Marketplace path %s must NOT be exposed via AcceptedStargateQueries", path)
	}
}
