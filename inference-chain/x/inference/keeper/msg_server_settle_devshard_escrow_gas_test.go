package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// The settled-accounts check is an existence check: settle gas must not depend on the summary's contents.
func TestSettleDevshardEscrow_GasIndependentOfPerformanceSummarySize(t *testing.T) {
	gasFor := func(counter uint64) storetypes.Gas {
		k, ms, ctx, mocks := setupDevshardEscrowTest(t)
		sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
		keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
		slots := make([]string, keeper.DevshardGroupSize)
		for i := range keys {
			key, err := dcrdsecp.GeneratePrivateKey()
			require.NoError(t, err)
			keys[i] = key
			slots[i] = cosmosAddressFromDcrdKey(key).String()
			setParticipantForDevshardTest(t, k, ctx, slots[i])
			require.NoError(t, k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
				EpochIndex: 5, ParticipantId: slots[i], InferenceCount: counter, MissedRequests: counter,
				EarnedCoins: counter, RewardedCoins: counter, BurnedCoins: counter,
			}))
		}
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
		setActiveParticipantsForDevshardTest(t, k, ctx, 5, slots...)

		creator := sdk.AccAddress(make([]byte, 20))
		creator[0] = 0xAC
		escrow := types.DevshardEscrow{Id: 1, Creator: creator.String(), Amount: 7_000_000_000, Slots: slots, EpochIndex: 5}
		_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
		require.NoError(t, err)
		msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, 100_000_000), 200_000_000)
		mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

		c := keeper.WithTxParamsCache(ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()))
		_, err = ms.SettleDevshardEscrow(c, msg)
		require.NoError(t, err)
		return c.GasMeter().GasConsumed()
	}
	small, large := gasFor(1), gasFor(1<<50)
	t.Logf("settle gas: small summaries %d, large summaries %d", small, large)
	require.Equal(t, small, large)
}
