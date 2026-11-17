package app_test

import (
	"testing"
	"time"

	"cosmossdk.io/core/appmodule"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/stretchr/testify/require"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Exercise the module registered by app wiring, not only the exported keeper:
// the SDK module receives a keeper value before post-injection configuration.
func TestMaintenanceSlashingModuleFreezesAbsentVotesAndResumes(t *testing.T) {
	testApp := createTestApp(t)
	ctx := testApp.NewUncachedContext(false, cmtproto.Header{ChainID: TallyTestChainID, Height: 10})
	validators, err := testApp.StakingKeeper.GetAllValidators(ctx)
	require.NoError(t, err)
	require.Len(t, validators, 1)
	validator := validators[0]
	consAddrBytes, err := validator.GetConsAddr()
	require.NoError(t, err)
	consAddr := sdk.ConsAddress(consAddrBytes)
	valAddr, err := sdk.ValAddressFromBech32(validator.OperatorAddress)
	require.NoError(t, err)
	participant := sdk.AccAddress(valAddr)

	params, err := testApp.InferenceKeeper.GetParams(ctx)
	require.NoError(t, err)
	params.MaintenanceParams = inferencetypes.DefaultMaintenanceParams()
	params.MaintenanceParams.MaintenanceEnabled = true
	params.MaintenanceParams.MaintenanceMaxConcurrentPowerBps = 10000
	require.NoError(t, testApp.InferenceKeeper.SetParams(ctx, params))
	require.NoError(t, testApp.InferenceKeeper.SetMaintenanceReservation(ctx, inferencetypes.MaintenanceReservation{
		ReservationId: 1, Participant: participant.String(), StartHeight: 10, DurationBlocks: 4,
		Status: inferencetypes.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED,
	}))
	require.NoError(t, testApp.InferenceKeeper.SetMaintenanceState(ctx, inferencetypes.MaintenanceState{
		Participant: participant.String(), ScheduledReservationId: 1,
	}))
	require.NoError(t, testApp.InferenceKeeper.MaintenanceScheduledIndex.Set(ctx, 1))
	require.NoError(t, testApp.InferenceKeeper.SetMaintenanceTransition(ctx, 10, 1, 1))
	require.NoError(t, testApp.InferenceKeeper.SetMaintenanceTransition(ctx, 14, 1, 2))
	require.NoError(t, testApp.SlashingKeeper.SetValidatorSigningInfo(ctx, consAddr,
		slashingtypes.NewValidatorSigningInfo(consAddr, 1, 0, time.Unix(0, 0), false, 0)))

	beginBlocker, ok := testApp.ModuleManager.Modules[slashingtypes.ModuleName].(appmodule.HasBeginBlocker)
	require.True(t, ok)
	processVote := func(height int64, flag cmtproto.BlockIDFlag, offset, missed int64) {
		t.Helper()
		voteCtx := ctx.WithBlockHeight(height).WithVoteInfos([]abci.VoteInfo{{
			Validator: abci.Validator{Address: consAddr, Power: 1}, BlockIdFlag: flag,
		}})
		require.NoError(t, beginBlocker.BeginBlock(voteCtx))
		info, err := testApp.SlashingKeeper.GetValidatorSigningInfo(voteCtx, consAddr)
		require.NoError(t, err)
		require.Equal(t, offset, info.IndexOffset, "signing bitmap at height %d", height)
		require.Equal(t, missed, info.MissedBlocksCounter, "missed signatures at height %d", height)
	}

	// Slashing runs before inference's maintenance transitions and accounts for
	// the previous block's vote. The vote before the window still counts.
	processVote(10, cmtproto.BlockIDFlagAbsent, 1, 1)
	require.NoError(t, testApp.InferenceKeeper.ProcessMaintenanceTransitions(ctx.WithBlockHeight(10)))
	require.True(t, testApp.InferenceKeeper.IsParticipantInActiveMaintenance(ctx, participant))
	processVote(11, cmtproto.BlockIDFlagAbsent, 1, 1)
	processVote(12, cmtproto.BlockIDFlagAbsent, 1, 1)
	// Signed votes still advance the bitmap during maintenance.
	processVote(13, cmtproto.BlockIDFlagCommit, 2, 1)
	processVote(14, cmtproto.BlockIDFlagAbsent, 2, 1)
	require.NoError(t, testApp.InferenceKeeper.ProcessMaintenanceTransitions(ctx.WithBlockHeight(14)))
	require.False(t, testApp.InferenceKeeper.IsParticipantInActiveMaintenance(ctx, participant))
	// The first vote after the covered interval resumes normal accounting.
	processVote(15, cmtproto.BlockIDFlagAbsent, 3, 2)
	processVote(16, cmtproto.BlockIDFlagCommit, 4, 2)
}
