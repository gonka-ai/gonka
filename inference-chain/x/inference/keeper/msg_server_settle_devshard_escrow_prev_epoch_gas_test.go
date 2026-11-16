package keeper_test

import (
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// A previous-epoch settle pays hosts directly: settle gas must not depend on the participant records.
func TestSettleDevshardEscrow_PreviousEpochGasIndependentOfParticipantSize(t *testing.T) {
	gasFor := func(url string) storetypes.Gas {
		k, ms, ctx, mocks := setupDevshardEscrowTest(t)
		sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
		keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
		slots := make([]string, keeper.DevshardGroupSize)
		for i := range keys {
			key, err := dcrdsecp.GeneratePrivateKey()
			require.NoError(t, err)
			keys[i] = key
			slots[i] = cosmosAddressFromDcrdKey(key).String()
			require.NoError(t, k.SetParticipant(ctx, types.Participant{
				Index: slots[i], Address: slots[i], Status: types.ParticipantStatus_ACTIVE,
				InferenceUrl: url, CurrentEpochStats: types.NewCurrentEpochStats(),
			}))
		}
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 6))
		setActiveParticipantsForDevshardTest(t, k, ctx, 6, slots...)

		creator := sdk.AccAddress(make([]byte, 20))
		creator[0] = 0xAC
		escrow := types.DevshardEscrow{Id: 1, Creator: creator.String(), Amount: 7_000_000_000, Slots: slots, EpochIndex: 5}
		_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
		require.NoError(t, err)
		msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, 100_000_000), 200_000_000)
		mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

		c := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		_, err = ms.SettleDevshardEscrow(c, msg)
		require.NoError(t, err)
		return c.GasMeter().GasConsumed()
	}
	short, long := gasFor("http://h:8000"), gasFor("http://"+strings.Repeat("h", 180)+":8000")
	t.Logf("previous-epoch settle gas: short url %d, long url %d", short, long)
	require.Equal(t, short, long)
}

// The existence check stays: a previous-epoch settle naming an unknown participant is rejected.
func TestSettleDevshardEscrow_PreviousEpochUnknownParticipantRejected(t *testing.T) {
	k, ms, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
	slots := make([]string, keeper.DevshardGroupSize)
	for i := range keys {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
		if i > 0 {
			setParticipantForDevshardTest(t, k, ctx, slots[i])
		}
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 6))
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xAC
	escrow := types.DevshardEscrow{Id: 1, Creator: creator.String(), Amount: 7_000_000_000, Slots: slots, EpochIndex: 5}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)
	msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, 100_000_000), 200_000_000)
	_, err = ms.SettleDevshardEscrow(ctx, msg)
	require.ErrorContains(t, err, "participant "+slots[0]+" not found")
}
