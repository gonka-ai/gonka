package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func setBridgeSigningFallbackEpochs(t *testing.T, k inferencekeeper.Keeper, ctx sdk.Context) {
	t.Helper()
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 10))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 10})
	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        9,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:  10,
		DkgPhase: blstypes.DKGPhase_DKG_PHASE_FAILED,
	}))
}

func TestGetBridgeSigningEpochID(t *testing.T) {
	tests := []struct {
		name          string
		phases        map[uint64]blstypes.DKGPhase
		effective     uint64
		want          uint64
		wantErrorText string
	}{
		{
			name:      "current signed",
			phases:    map[uint64]blstypes.DKGPhase{9: blstypes.DKGPhase_DKG_PHASE_SIGNED, 10: blstypes.DKGPhase_DKG_PHASE_SIGNED},
			effective: 10,
			want:      10,
		},
		{
			name:      "failed current epoch",
			phases:    map[uint64]blstypes.DKGPhase{9: blstypes.DKGPhase_DKG_PHASE_SIGNED, 10: blstypes.DKGPhase_DKG_PHASE_FAILED},
			effective: 10,
			want:      9,
		},
		{
			name:      "completed current epoch",
			phases:    map[uint64]blstypes.DKGPhase{9: blstypes.DKGPhase_DKG_PHASE_SIGNED, 10: blstypes.DKGPhase_DKG_PHASE_COMPLETED},
			effective: 10,
			want:      9,
		},
		{
			name:      "missing current epoch",
			phases:    map[uint64]blstypes.DKGPhase{9: blstypes.DKGPhase_DKG_PHASE_SIGNED},
			effective: 10,
			want:      9,
		},
		{
			name:          "fallback age exceeded",
			phases:        map[uint64]blstypes.DKGPhase{8: blstypes.DKGPhase_DKG_PHASE_SIGNED, 9: blstypes.DKGPhase_DKG_PHASE_FAILED, 10: blstypes.DKGPhase_DKG_PHASE_COMPLETED},
			effective:     10,
			wantErrorText: "no eligible BLS signing epoch",
		},
		{
			name:          "no signed epoch",
			phases:        map[uint64]blstypes.DKGPhase{9: blstypes.DKGPhase_DKG_PHASE_FAILED, 10: blstypes.DKGPhase_DKG_PHASE_FAILED},
			effective:     10,
			wantErrorText: "no eligible BLS signing epoch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
			blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
			require.True(t, ok)
			for epochID, phase := range tt.phases {
				require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
					EpochId:        epochID,
					DkgPhase:       phase,
					GroupPublicKey: []byte{1},
				}))
			}

			got, err := k.GetBridgeSigningEpochID(ctx, tt.effective)
			if tt.wantErrorText != "" {
				require.ErrorContains(t, err, tt.wantErrorText)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGetBridgeSigningEpochID_CompletedRecovery(t *testing.T) {
	tests := []struct {
		name          string
		height        int64
		deadline      int64
		timeout       int64
		previousPhase blstypes.DKGPhase
		want          uint64
	}{
		{"before timeout uses signed predecessor", 109, 100, 10, blstypes.DKGPhase_DKG_PHASE_SIGNED, 9},
		{"at timeout prefers current", 110, 100, 10, blstypes.DKGPhase_DKG_PHASE_SIGNED, 10},
		{"after timeout prefers current", 111, 100, 10, blstypes.DKGPhase_DKG_PHASE_SIGNED, 10},
		{"recovery after failed predecessor", 110, 100, 10, blstypes.DKGPhase_DKG_PHASE_FAILED, 10},
		{"disabled timeout", 110, 100, 0, blstypes.DKGPhase_DKG_PHASE_FAILED, 0},
		{"missing deadline", 110, 0, 10, blstypes.DKGPhase_DKG_PHASE_FAILED, 0},
		{"completed predecessor cannot extend fallback", 109, 100, 10, blstypes.DKGPhase_DKG_PHASE_COMPLETED, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
			ctx = ctx.WithBlockHeight(tt.height)
			blsK := k.BlsKeeper.(blskeeper.Keeper)
			params, err := blsK.GetParams(ctx)
			require.NoError(t, err)
			params.CompletedFallbackBlocks = tt.timeout
			require.NoError(t, blsK.SetParams(ctx, params))
			require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
				EpochId: 9, DkgPhase: tt.previousPhase, GroupPublicKey: []byte{1}, DisputingPhaseDeadlineBlock: 1,
			}))
			require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
				EpochId: 10, DkgPhase: blstypes.DKGPhase_DKG_PHASE_COMPLETED, GroupPublicKey: []byte{1}, DisputingPhaseDeadlineBlock: tt.deadline,
			}))
			got, err := k.GetBridgeSigningEpochID(ctx, 10)
			if tt.want == 0 {
				require.ErrorContains(t, err, "no eligible BLS signing epoch")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
				CurrentEpochId: got, RequestId: make([]byte, 32), ChainId: make([]byte, 32),
			}))
		})
	}
}
