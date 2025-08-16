package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

func TestTransferExemptions(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Set up test exemptions
	params := types.DefaultParams()
	params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{
		{
			ExemptionId:   "exemption1",
			FromAddress:   "cosmos1test1",
			ToAddress:     "cosmos1test2",
			MaxAmount:     "1000000",
			UsageLimit:    5,
			ExpiryBlock:   2000000, // Future expiry
			Justification: "Emergency test 1",
		},
		{
			ExemptionId:   "exemption2",
			FromAddress:   "*",
			ToAddress:     "cosmos1test3",
			MaxAmount:     "500000",
			UsageLimit:    10,
			ExpiryBlock:   500000, // Past expiry
			Justification: "Emergency test 2",
		},
	}
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Set current block height
	ctx = ctx.WithBlockHeight(1000000)

	// Test without including expired exemptions
	resp, err := keeper.TransferExemptions(ctx, &types.QueryTransferExemptionsRequest{
		IncludeExpired: false,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Should only return active exemptions (exemption1)
	require.Len(t, resp.Exemptions, 1)
	require.Equal(t, "exemption1", resp.Exemptions[0].ExemptionId)

	// Test including expired exemptions
	resp, err = keeper.TransferExemptions(ctx, &types.QueryTransferExemptionsRequest{
		IncludeExpired: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Should return all exemptions
	require.Len(t, resp.Exemptions, 2)
}

func TestTransferExemptionsNilRequest(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	resp, err := keeper.TransferExemptions(ctx, nil)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "invalid request")
}
