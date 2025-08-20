package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

func TestTransferRestrictionStatus(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	// Test when restrictions are active
	params := types.DefaultParams()
	params.RestrictionEndBlock = 2000000 // Set end block in the future
	err := keeper.SetParams(ctx, params)
	require.NoError(t, err)

	// Mock current block height to be less than restriction end
	ctx = ctx.WithBlockHeight(1000000)

	resp, err := keeper.TransferRestrictionStatus(ctx, &types.QueryTransferRestrictionStatusRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response when restrictions are active
	require.True(t, resp.IsActive)
	require.Equal(t, uint64(2000000), resp.RestrictionEndBlock)
	require.Equal(t, uint64(1000000), resp.CurrentBlockHeight)
	require.Equal(t, uint64(1000000), resp.RemainingBlocks) // 2000000 - 1000000

	// Test when restrictions are inactive
	ctx = ctx.WithBlockHeight(2500000) // Set current block higher than restriction end

	resp, err = keeper.TransferRestrictionStatus(ctx, &types.QueryTransferRestrictionStatusRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response when restrictions are inactive
	require.False(t, resp.IsActive)
	require.Equal(t, uint64(2000000), resp.RestrictionEndBlock)
	require.Equal(t, uint64(2500000), resp.CurrentBlockHeight)
	require.Equal(t, uint64(0), resp.RemainingBlocks) // Should be 0 when inactive
}

func TestTransferRestrictionStatusNilRequest(t *testing.T) {
	keeper, ctx := keepertest.RestrictionsKeeper(t)

	resp, err := keeper.TransferRestrictionStatus(ctx, nil)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "invalid request")
}
