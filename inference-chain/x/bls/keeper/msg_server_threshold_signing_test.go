package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func setupMsgServerThresholdSigning(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context) {
	k, ctx := keepertest.BlsKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func setCompletedEpoch(t testing.TB, k keeper.Keeper, ctx sdk.Context, epochID uint64) {
	t.Helper()
	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: []byte{1, 2, 3},
	})
	require.NoError(t, err)
}

func TestCurrentSigningEpochID_SetGet(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	epochID, found := k.GetCurrentSigningEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), epochID)

	k.SetCurrentSigningEpochID(ctx, 7)

	epochID, found = k.GetCurrentSigningEpochID(ctx)
	require.True(t, found)
	require.Equal(t, uint64(7), epochID)
}
