package keeper_test

import (
	"strings"
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestIsPoCParticipantBlocked_MatchesAccountBytes(t *testing.T) {
	k, ctx := auditKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		BlockedParticipantAddresses: []string{
			"not-an-address",
			strings.ToUpper(testutil.Validator),
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.True(t, k.IsPoCParticipantBlocked(ctx, testutil.Validator))
	require.True(t, k.IsPoCParticipantBlocked(ctx, strings.ToUpper(testutil.Validator)))
	require.False(t, k.IsPoCParticipantBlocked(ctx, testutil.Executor))
	require.False(t, k.IsPoCParticipantBlocked(ctx, "not-an-address"))
}

func TestMLNodeWeightDistribution_UppercaseCreatorUsesCanonicalAccount(t *testing.T) {
	k, sdkCtx, ms := setupWeightDistribution(t)
	upper := strings.ToUpper(testutil.Validator)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		BlockedParticipantAddresses: []string{testutil.Validator},
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	blocked := &types.MsgMLNodeWeightDistribution{
		Creator:                  upper,
		PocStageStartBlockHeight: 100,
		Entries: []*types.MLNodeDistributionEntry{{
			ModelId: testPoCModelID,
			Weights: []*types.MLNodeWeight{{NodeId: "a", Weight: 10000}},
		}},
	}
	_, err = ms.MLNodeWeightDistribution(sdkCtx, blocked)
	require.ErrorIs(t, err, types.ErrParticipantBlocked)

	params.ParticipantAccessParams.BlockedParticipantAddresses = nil
	require.NoError(t, k.SetParams(sdkCtx, params))
	_, err = ms.MLNodeWeightDistribution(sdkCtx, blocked)
	require.NoError(t, err)

	stored, err := k.GetMLNodeWeightDistribution(sdkCtx, 100, testutil.Validator, testPoCModelID)
	require.NoError(t, err)
	require.Equal(t, testutil.Validator, stored.ParticipantAddress)
}
