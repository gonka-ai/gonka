package keeper_test

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const forfeitedDevshardMemo = "forfeited_work_coins_to_governance:epoch=5"

type forfeitedDevshardResult struct {
	err              error
	participant      types.Participant
	escrow           types.DevshardEscrow
	governanceCalled bool
	hostStatsFound   bool
}

func settleExcludedDevshardEscrow(
	t *testing.T,
	currentEpoch uint64,
	accountsSettled bool,
	governanceTransferErr error,
) forfeitedDevshardResult {
	t.Helper()

	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	key, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	participantAddr := cosmosAddressFromDcrdKey(key)
	participant := participantAddr.String()
	setParticipantForDevshardTest(t, k, ctx, participant)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch))
	setActiveParticipantsForDevshardTest(t, k, ctx, currentEpoch, participant)

	const escrowEpoch = uint64(5)
	require.NoError(t, k.ExcludedParticipantsMap.Set(ctx, collections.Join(escrowEpoch, participantAddr), types.ExcludedParticipant{
		Address:    participant,
		EpochIndex: escrowEpoch,
		Reason:     "test exclusion",
	}))
	if accountsSettled {
		require.NoError(t, k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
			EpochIndex:    escrowEpoch,
			ParticipantId: participant,
		}))
	}

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x51
	slots := make([]string, keeper.DevshardGroupSize)
	keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
	for i := range slots {
		slots[i] = participant
		keys[i] = key
	}
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: escrowEpoch,
	}
	_, err = k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	const costPerSlot = uint64(100_000_000)
	const fees = uint64(200_000_000)
	msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, costPerSlot), fees)
	expectedPayout := uint64(keeper.DevshardGroupSize)*costPerSlot + fees
	expectedCoins, err := types.GetCoins(int64(expectedPayout))
	require.NoError(t, err)

	governanceCalled := false
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToModule(gomock.Any(), types.ModuleName, govtypes.ModuleName, expectedCoins, forfeitedDevshardMemo).
		DoAndReturn(func(_ context.Context, _, _ string, _ sdk.Coins, _ string) error {
			governanceCalled = true
			return governanceTransferErr
		})
	if governanceTransferErr == nil {
		expectedRefund := escrow.Amount - expectedPayout
		mocks.BankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
			DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
				require.Len(t, coins, 1)
				require.Equal(t, expectedRefund, coins[0].Amount.Uint64())
				return nil
			})
	}

	_, settleErr := ms.SettleDevshardEscrow(ctx, msg)
	updatedParticipant, found := k.GetParticipant(ctx, participant)
	require.True(t, found)
	updatedEscrow, found := k.GetDevshardEscrow(ctx, escrow.Id)
	require.True(t, found)
	_, hostStatsFound := k.GetDevshardHostEpochStats(ctx, escrowEpoch, participantAddr)

	return forfeitedDevshardResult{
		err:              settleErr,
		participant:      updatedParticipant,
		escrow:           updatedEscrow,
		governanceCalled: governanceCalled,
		hostStatsFound:   hostStatsFound,
	}
}

func TestSettleDevshardEscrow_CurrentEpochExcludedPayoutGoesToGovernance(t *testing.T) {
	result := settleExcludedDevshardEscrow(t, 5, false, nil)

	require.NoError(t, result.err)
	require.True(t, result.governanceCalled)
	require.Zero(t, result.participant.CoinBalance)
	require.True(t, result.escrow.Settled)
	require.True(t, result.hostStatsFound)
}

func TestSettleDevshardEscrow_ExcludedPayoutAfterAccountSettlementGoesToGovernance(t *testing.T) {
	result := settleExcludedDevshardEscrow(t, 5, true, nil)

	require.NoError(t, result.err)
	require.True(t, result.governanceCalled)
	require.Zero(t, result.participant.CoinBalance)
	require.True(t, result.escrow.Settled)
}

func TestSettleDevshardEscrow_PreviousEpochExclusionOverridesCurrentActiveStatus(t *testing.T) {
	result := settleExcludedDevshardEscrow(t, 6, false, nil)

	require.NoError(t, result.err)
	require.True(t, result.governanceCalled)
	require.Equal(t, types.ParticipantStatus_ACTIVE, result.participant.Status)
	require.Zero(t, result.participant.CoinBalance)
	require.True(t, result.escrow.Settled)
}

func TestSettleDevshardEscrow_GovernanceTransferFailureLeavesEscrowUnsettled(t *testing.T) {
	transferErr := errors.New("governance transfer failed")
	result := settleExcludedDevshardEscrow(t, 5, false, transferErr)

	require.ErrorIs(t, result.err, transferErr)
	require.True(t, result.governanceCalled)
	require.Zero(t, result.participant.CoinBalance)
	require.False(t, result.escrow.Settled)
	require.False(t, result.hostStatsFound)
}
