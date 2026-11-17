package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Mainnet timing: PoC 35, delay 5, validation 240, set-new-validators 120,
// epoch 15391 blocks, delegation snapshot 500 blocks before the next PoC.
func mainnetTimingParams() types.Params {
	p := types.DefaultParams()
	p.EpochParams.EpochLength = 15391
	p.EpochParams.PocStageDuration = 35
	p.EpochParams.PocExchangeDuration = 0
	p.EpochParams.PocValidationDelay = 5
	p.EpochParams.PocValidationDuration = 240
	p.EpochParams.SetNewValidatorsDelay = 120
	p.DelegationParams.DeployWindow = 500
	return p
}

const pocStart = int64(100_000)

func setupEpochTiming(t *testing.T) (keeper.Keeper, types.MsgServer, sdk.Context) {
	k, ms, ctx := setupMsgServer(t)
	require.NoError(t, k.SetParams(ctx, mainnetTimingParams()))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 7, PocStartBlockHeight: pocStart}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))
	return k, ms, ctx
}

func updateAt(ms types.MsgServer, k keeper.Keeper, ctx sdk.Context, height int64, p types.Params) error {
	_, err := ms.UpdateParams(ctx.WithBlockHeight(height), &types.MsgUpdateParams{Authority: k.GetAuthority(), Params: p})
	return err
}

func TestEpochTimingChange_ShorterEpochPastNextPoCStartRejected(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	p := mainnetTimingParams()
	p.EpochParams.EpochLength = 10_000
	// Next PoC start moves to 110000, already behind the chain: no stage would fire again.
	require.ErrorContains(t, updateAt(ms, k, ctx, pocStart+12_000, p), "epoch timing params")
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(15391), got.EpochParams.EpochLength)
}

func TestEpochTimingChange_ShorterValidationDuringValidationRejected(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	p := mainnetTimingParams()
	p.EpochParams.PocValidationDuration = 120
	// End of validation moves from +280 to +160 while the chain is at +200.
	require.ErrorContains(t, updateAt(ms, k, ctx, pocStart+200, p), "epoch timing params")
}

func TestEpochTimingChange_InferencePhaseAccepted(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	p := mainnetTimingParams()
	p.EpochParams.EpochLength = 10_000
	p.EpochParams.PocValidationDuration = 120
	require.NoError(t, updateAt(ms, k, ctx, pocStart+5_000, p))
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10_000), got.EpochParams.EpochLength)
}

func TestEpochTimingChange_InsideDelegationWindowRejected(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	p := mainnetTimingParams()
	p.EpochParams.EpochLength = 15_400
	require.ErrorContains(t, updateAt(ms, k, ctx, pocStart+15_391-100, p), "epoch timing params")
}

func TestEpochTimingChange_OtherParamsAnytime(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	p := mainnetTimingParams()
	p.EpochParams.InferencePruningMax = 1234
	require.NoError(t, updateAt(ms, k, ctx, pocStart+200, p))
}

func TestEpochTimingChange_ActiveConfirmationPoCRejected(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{EpochIndex: 7, TriggerHeight: pocStart + 5_000}))
	p := mainnetTimingParams()
	p.EpochParams.PocStageDuration = 60
	require.ErrorContains(t, updateAt(ms, k, ctx, pocStart+5_010, p), "confirmation PoC")
}

func TestEpochTimingChange_LongerEpochDuringNextPoCRejected(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	// PoC of epoch 8 has started, epoch 7 is still effective: a longer epoch
	// would move epoch 8's PoC start ahead and fire it a second time (#344).
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 8, PocStartBlockHeight: pocStart + 15_391}))
	p := mainnetTimingParams()
	p.EpochParams.EpochLength = 15_500
	require.ErrorContains(t, updateAt(ms, k, ctx, pocStart+15_391+100, p), "epoch timing params")
}

func TestEpochTimingChange_ShorterDeployWindowPastSnapshotRejected(t *testing.T) {
	k, ms, ctx := setupEpochTiming(t)
	p := mainnetTimingParams()
	p.DelegationParams.DeployWindow = 300
	// The snapshot height moves from next PoC -500 to -300 after the chain passed -400.
	require.ErrorContains(t, updateAt(ms, k, ctx, pocStart+15_391-400, p), "epoch timing params")
}
