package keeper_test

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	forfeitedWorkCoinsEpoch   = uint64(10)
	forfeitedWorkCoinsBalance = int64(1000)
	forfeitedWorkCoinsMemo    = "forfeited_work_coins_to_governance:epoch=10"
)

type forfeitedWorkCoinsResult struct {
	err             error
	coinBalance     int64
	earnedCoins     uint64
	summaryFound    bool
	settleAmount    types.SettleAmount
	settleFound     bool
	governanceCalls int
}

func settleParticipantWorkCoins(t *testing.T, status types.ParticipantStatus, transferErr error) forfeitedWorkCoinsResult {
	t.Helper()

	participant := types.Participant{
		Index:       testutil.Executor,
		Address:     testutil.Executor,
		CoinBalance: forfeitedWorkCoinsBalance,
		Status:      status,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
		},
	}

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetParticipant(ctx, participant))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: forfeitedWorkCoinsEpoch,
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress:      participant.Address,
			Weight:             1000,
			Reputation:         100,
			ConfirmationWeight: 1000,
		}},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: forfeitedWorkCoinsEpoch,
		Participants: []*types.ActiveParticipant{{
			Index: participant.Address,
		}},
	}))

	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	expectedCoins, err := types.GetCoins(forfeitedWorkCoinsBalance)
	require.NoError(t, err)
	governanceCalls := 0
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, senderModule, recipientModule string, coins sdk.Coins, memo string) error {
			if memo != forfeitedWorkCoinsMemo {
				return nil
			}
			governanceCalls++
			require.Equal(t, types.ModuleName, senderModule)
			require.Equal(t, govtypes.ModuleName, recipientModule)
			require.Equal(t, expectedCoins, coins)
			return transferErr
		}).AnyTimes()

	_, settleErr := k.SettleAccounts(ctx, forfeitedWorkCoinsEpoch, 0)
	updated, found := k.GetParticipant(ctx, participant.Address)
	require.True(t, found)
	summary, summaryFound := k.GetEpochPerformanceSummary(ctx, forfeitedWorkCoinsEpoch, participant.Address)
	settleAmount, settleFound := k.GetSettleAmount(ctx, participant.Address)

	return forfeitedWorkCoinsResult{
		err:             settleErr,
		coinBalance:     updated.CoinBalance,
		earnedCoins:     summary.EarnedCoins,
		summaryFound:    summaryFound,
		settleAmount:    settleAmount,
		settleFound:     settleFound,
		governanceCalls: governanceCalls,
	}
}

func TestSettleAccounts_ActiveParticipantWorkCoinsRemainClaimable(t *testing.T) {
	result := settleParticipantWorkCoins(t, types.ParticipantStatus_ACTIVE, nil)

	require.NoError(t, result.err)
	require.Zero(t, result.governanceCalls)
	require.Zero(t, result.coinBalance)
	require.True(t, result.summaryFound)
	require.Equal(t, uint64(forfeitedWorkCoinsBalance), result.earnedCoins)
	require.True(t, result.settleFound)
	require.Equal(t, uint64(forfeitedWorkCoinsBalance), result.settleAmount.WorkCoins)
}

func TestSettleAccounts_InvalidParticipantWorkCoinsGoToGovernance(t *testing.T) {
	result := settleParticipantWorkCoins(t, types.ParticipantStatus_INVALID, nil)

	require.NoError(t, result.err)
	require.Equal(t, 1, result.governanceCalls)
	require.Zero(t, result.coinBalance)
	require.True(t, result.summaryFound)
	require.Zero(t, result.earnedCoins)
	require.False(t, result.settleFound)
}

func TestSettleAccounts_InactiveParticipantWorkCoinsGoToGovernance(t *testing.T) {
	result := settleParticipantWorkCoins(t, types.ParticipantStatus_INACTIVE, nil)

	require.NoError(t, result.err)
	require.Equal(t, 1, result.governanceCalls)
	require.Zero(t, result.coinBalance)
	require.True(t, result.summaryFound)
	require.Zero(t, result.earnedCoins)
	require.False(t, result.settleFound)
}

func TestSettleAccounts_ForfeitedWorkCoinsTransferFailureRollsBack(t *testing.T) {
	transferErr := errors.New("governance transfer failed")
	result := settleParticipantWorkCoins(t, types.ParticipantStatus_INVALID, transferErr)

	require.ErrorIs(t, result.err, transferErr)
	require.Equal(t, 1, result.governanceCalls)
	require.Equal(t, forfeitedWorkCoinsBalance, result.coinBalance)
	require.False(t, result.summaryFound)
	require.False(t, result.settleFound)
}
