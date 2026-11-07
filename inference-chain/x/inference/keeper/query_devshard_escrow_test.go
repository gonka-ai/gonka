package keeper_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestDevshardEscrowQuery_HeightsyncTimeoutClock(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)

	escrow := &types.DevshardEscrow{
		Creator:                "gonka1creator",
		Amount:                 1_000,
		Slots:                  []string{"a", "b", "c"},
		EpochIndex:             5,
		ModelId:                testDevshardModelID,
		HeightsyncTimeoutClock: true,
	}
	id, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	resp, err := k.DevshardEscrow(ctx, &types.QueryGetDevshardEscrowRequest{Id: id})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.NotNil(t, resp.Escrow)
	require.True(t, resp.Escrow.HeightsyncTimeoutClock)

	bz, err := json.Marshal(resp.Escrow)
	require.NoError(t, err)
	require.Contains(t, string(bz), `"heightsync_timeout_clock":true`)
}

func TestDevshardEscrowQuery_LegacyHeightsyncTimeoutClockFalse(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)

	escrow := &types.DevshardEscrow{
		Creator:    "gonka1creator",
		Amount:     1_000,
		Slots:      []string{"a", "b", "c"},
		EpochIndex: 5,
		ModelId:    testDevshardModelID,
		// HeightsyncTimeoutClock omitted → false
	}
	id, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	resp, err := k.DevshardEscrow(ctx, &types.QueryGetDevshardEscrowRequest{Id: id})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.False(t, resp.Escrow.HeightsyncTimeoutClock)
}
