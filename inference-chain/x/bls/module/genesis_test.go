package bls_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	bls "github.com/productscience/inference/x/bls/module"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	params := types.DefaultParams()
	params.MaxSigningAttempts = 1

	genesisState := types.GenesisState{
		Params:                params,
		ActiveEpochId:         2,
		CurrentSigningEpochId: 2,
		BlsDataList: []types.EpochBLSData{
			{
				EpochId:     1,
				ITotalSlots: 10,
				Participants: []types.BLSParticipantInfo{
					{
						Address:        "testing_address",
						SlotStartIndex: 1,
						SlotEndIndex:   10,
					},
				},
			},
		},
		SigningRequests: []types.ThresholdSigningRequest{
			{
				RequestId:           []byte("request1"),
				CurrentEpochId:      1,
				ChainId:             []byte("chain1"),
				Data:                [][]byte{[]byte("data1")},
				Status:              types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
				DeadlineBlockHeight: 100,
				Attempt:             1,
			},
		},
		GroupValidationStates: []types.GroupKeyValidationState{
			{
				NewEpochId:      2,
				PreviousEpochId: 1,
				Status:          types.GroupKeyValidationStatus_GROUP_KEY_VALIDATION_STATUS_COLLECTING_SIGNATURES,
			},
		},
		// this line is used by starport scaffolding # genesis/test/state
	}

	k, ctx := keepertest.BlsKeeper(t)
	bls.InitGenesis(ctx, k, genesisState)
	got := bls.ExportGenesis(ctx, k)
	require.NotNil(t, got)

	nullify.Fill(&genesisState)
	nullify.Fill(got)

	require.Equal(t, genesisState.ActiveEpochId, got.ActiveEpochId)
	require.Equal(t, genesisState.CurrentSigningEpochId, got.CurrentSigningEpochId)
	require.ElementsMatch(t, genesisState.BlsDataList, got.BlsDataList)
	require.ElementsMatch(t, genesisState.SigningRequests, got.SigningRequests)
	require.ElementsMatch(t, genesisState.GroupValidationStates, got.GroupValidationStates)

	// Test behavioral rebuilding of the Expiration Index
	// Force advance block height to exactly the deadline (100)
	// because ProcessThresholdSigningDeadlines scans exactly the current block height
	ctx = ctx.WithBlockHeight(100)
	err := k.ProcessThresholdSigningDeadlines(ctx)
	require.NoError(t, err)

	// Verify request1 is now marked as EXPIRED by the process
	req, err := k.GetSigningStatus(ctx, []byte("request1"))
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, req.Status)

	// this line is used by starport scaffolding # genesis/test/assert
}

// A chain started from genesis runs the beacon from its first block and keys
// it with the newest completed DKG among the imported epochs.
func TestGenesisActivatesBlockBeacon(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)
	gs := types.GenesisState{
		Params: types.DefaultParams(),
		BlsDataList: []types.EpochBLSData{
			{EpochId: 3, ITotalSlots: 10, DkgPhase: types.DKGPhase_DKG_PHASE_SIGNED},
			{EpochId: 4, ITotalSlots: 10, DkgPhase: types.DKGPhase_DKG_PHASE_COMPLETED},
			{EpochId: 5, ITotalSlots: 10, DkgPhase: types.DKGPhase_DKG_PHASE_DEALING},
		},
	}
	bls.InitGenesis(ctx, k, gs)

	a, ok := k.GetBeaconActivation(ctx)
	require.True(t, ok)
	require.Zero(t, a.ExtFrom)
	require.Zero(t, a.InjFrom)
	require.Zero(t, a.RecFrom)
	key, ok := k.GetBeaconKeyEpoch(ctx)
	require.True(t, ok)
	require.Equal(t, uint64(4), key)

	// Empty genesis: live, no key until the first DKG completes.
	k2, ctx2 := keepertest.BlsKeeper(t)
	bls.InitGenesis(ctx2, k2, *types.DefaultGenesis())
	_, ok = k2.GetBeaconActivation(ctx2)
	require.True(t, ok)
	_, ok = k2.GetBeaconKeyEpoch(ctx2)
	require.False(t, ok)
}
