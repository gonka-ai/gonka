package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// mainnetLikeParticipant mirrors an ACTIVE mainnet record (h=6417019).
func mainnetLikeParticipant(addr string) types.Participant {
	return types.Participant{
		Index: addr, Address: addr, Weight: -1, JoinTime: 1765041275754, JoinHeight: 1608147,
		LastInferenceTime: 1765140412391, InferenceUrl: "http://95.133.252.105:8000",
		Status: types.ParticipantStatus_ACTIVE, ValidatorKey: "/m43OwvHxD3R6BGC9r2GbsT8IRRPiZ7jwkKjA0fC0HU=",
		EpochsCompleted: 1, CurrentEpochStats: types.NewCurrentEpochStats(),
	}
}

func TestParticipant_ValueOmitsKeyFields(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	p := mainnetLikeParticipant(testutil.Executor)

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetParticipant(gctx, p))
	t.Logf("SetParticipant gas: %d", gctx.GasMeter().GasConsumed())
	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, found := k.GetParticipant(gctx, testutil.Executor)
	require.True(t, found)
	t.Logf("GetParticipant gas: %d", gctx.GasMeter().GasConsumed())

	raw, err := k.Participants.Get(ctx, addr)
	require.NoError(t, err)
	require.Empty(t, raw.Index)
	require.Empty(t, raw.Address)

	want, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, testutil.Executor, want.Index)
	require.Equal(t, testutil.Executor, want.Address)
	require.Equal(t, p.InferenceUrl, want.InferenceUrl)

	all := k.GetAllParticipant(ctx)
	require.Len(t, all, 1)
	require.Equal(t, want, all[0])

	res, err := k.ParticipantAll(ctx, &types.QueryAllParticipantRequest{})
	require.NoError(t, err)
	require.Equal(t, []types.Participant{want}, res.Participant)

	mocks.BankViewKeeper.EXPECT().GetAllBalances(gomock.Any(), addr).Return(sdk.NewCoins())
	withBal, err := k.ParticipantsWithBalances(ctx, &types.QueryParticipantsWithBalancesRequest{})
	require.NoError(t, err)
	require.Len(t, withBal.Participants, 1)
	require.Equal(t, want, withBal.Participants[0].Participant)
}

func TestParticipant_LegacyFullRecord(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	legacy := mainnetLikeParticipant(testutil.Executor)
	require.NoError(t, k.Participants.Set(ctx, addr, legacy))

	got, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, legacy, got)

	got.CoinBalance = 7
	require.NoError(t, k.SetParticipant(ctx, got))
	raw, err := k.Participants.Get(ctx, addr)
	require.NoError(t, err)
	require.Empty(t, raw.Index)
	got2, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, testutil.Executor, got2.Address)
	require.Equal(t, int64(7), got2.CoinBalance)
}

func TestParticipant_AddressDifferentFromIndexStoredWhole(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	p := mainnetLikeParticipant(testutil.Executor)
	p.Address = testutil.Executor2
	require.NoError(t, k.Participants.Set(ctx, addr, p))
	require.NoError(t, k.SetParticipant(ctx, p))

	raw, err := k.Participants.Get(ctx, addr)
	require.NoError(t, err)
	require.Equal(t, testutil.Executor, raw.Index)
	require.Equal(t, testutil.Executor2, raw.Address)
	got, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, testutil.Executor2, got.Address)
}
