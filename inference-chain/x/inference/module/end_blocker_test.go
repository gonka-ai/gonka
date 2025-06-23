package inference_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

func TestEndBlockerEpochTransition(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	am := inference.NewAppModule(nil, k, mocks.AccountKeeper, mocks.BankKeeper, mocks.GroupKeeper)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// 1. Setup initial state
	params := types.DefaultParams()
	params.EpochParams.PocNumBlocks = 100
	params.EpochParams.SetValidatorsNumBlocks = 10
	k.SetParams(sdkCtx, params)

	// Set an initial effective epoch
	initialEpoch := &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 1,
	}
	k.SetEffectiveEpoch(sdkCtx, initialEpoch)

	// Set an upcoming epoch group that will become effective
	upcomingPocStartBlockHeight := initialEpoch.PocStartBlockHeight + int64(params.EpochParams.PocNumBlocks)
	upcomingEg, err := k.GetOrCreateEpochGroup(sdkCtx, uint64(upcomingPocStartBlockHeight), "")
	require.NoError(t, err)

	// Create a dummy participant to be part of the new validator set
	participant := types.Participant{
		Index:        "participant1",
		ValidatorKey: "validatorKey1",
	}
	k.SetParticipant(sdkCtx, participant)
	k.SetPocBatch(sdkCtx, types.PoCBatch{
		ParticipantAddress:       participant.Index,
		PocStageStartBlockHeight: uint64(upcomingPocStartBlockHeight),
	})
	k.SetRandomSeed(sdkCtx, types.RandomSeed{
		Participant: participant.Index,
		BlockHeight: uint64(upcomingPocStartBlockHeight),
		Signature:   "signature",
	})

	// The upcoming group needs a validator from the previous epoch to validate participants
	currentEg, err := k.GetCurrentEpochGroup(sdkCtx)
	require.NoError(t, err)
	currentEg.GroupData.ValidationWeights = []*types.ValidationWeight{
		{MemberAddress: "validator1", Weight: 1},
	}
	k.SetEpochGroupData(sdkCtx, *currentEg.GroupData)

	k.SetPoCValidation(sdkCtx, types.PoCValidation{
		ParticipantAddress:          participant.Index,
		ValidatorParticipantAddress: "validator1",
		PocStageStartBlockHeight:    uint64(upcomingPocStartBlockHeight),
		FraudDetected:               false,
	})

	// 2. Advance block height to trigger onSetNewValidatorsStage
	// It's start of PoC + PocNumBlocks + SetValidatorsNumBlocks
	newValidatorsStageHeight := initialEpoch.PocStartBlockHeight + int64(params.EpochParams.PocNumBlocks) + int64(params.EpochParams.SetValidatorsNumBlocks)
	sdkCtx = sdkCtx.WithBlockHeight(newValidatorsStageHeight)
	sdkCtx = sdkCtx.WithBlockTime(time.Now())

	// Mock dependencies
	mocks.StakingKeeper.EXPECT().SetComputeValidators(gomock.Any(), gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetModuleAccount(gomock.Any(), gomock.Any()).Return(nil).AnyTimes() // For settlement

	// 3. Call EndBlock
	err = am.EndBlock(sdkCtx)
	require.NoError(t, err)

	// 4. Assertions
	// Check that the effective epoch has been updated
	effectiveEpoch, found := k.GetEffectiveEpoch(sdkCtx)
	require.True(t, found)
	require.Equal(t, uint64(2), effectiveEpoch.Index, "The effective epoch index should be incremented")

	// Check that the upcoming epoch group has become the current one
	// and has members.
	currentEg, err = k.GetCurrentEpochGroup(sdkCtx)
	require.NoError(t, err)
	require.Equal(t, uint64(upcomingPocStartBlockHeight), currentEg.GroupData.PocStartBlockHeight)
	require.True(t, currentEg.IsChanged(sdkCtx), "the new epoch group should be marked as changed")

	// Call EndBlock again to trigger setting compute validators
	err = am.EndBlock(sdkCtx)
	require.NoError(t, err)

	require.False(t, currentEg.IsChanged(sdkCtx), "the new epoch group should be marked as unchanged after setting validators")
}
