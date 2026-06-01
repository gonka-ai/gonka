package keeper_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func TestRequestThresholdSignature_RequiresSignedBeforeFallback(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.CompletedFallbackBlocks = 10
	require.NoError(t, k.SetParams(ctx, params))

	const epochID = uint64(77)
	const disputingDeadlineBlock = int64(100)
	ctx = ctx.WithBlockHeight(disputingDeadlineBlock + params.CompletedFallbackBlocks - 1)

	err = k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:                     epochID,
		DkgPhase:                    types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey:              []byte{1},
		DisputingPhaseDeadlineBlock: disputingDeadlineBlock,
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, buildSigningData(epochID, 1))
	require.Error(t, err)
	require.Contains(t, err.Error(), "fallback available at block")
}

func TestRequestThresholdSignature_AllowsCompletedAfterFallback(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.CompletedFallbackBlocks = 10
	require.NoError(t, k.SetParams(ctx, params))

	const epochID = uint64(78)
	const disputingDeadlineBlock = int64(100)
	ctx = ctx.WithBlockHeight(disputingDeadlineBlock + params.CompletedFallbackBlocks)

	signingData := buildSigningData(epochID, 10)
	err = k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:                     epochID,
		DkgPhase:                    types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey:              []byte{1},
		DisputingPhaseDeadlineBlock: disputingDeadlineBlock,
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, signingData)
	require.NoError(t, err)

	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, request.Status)
	require.Equal(t, epochID, request.CurrentEpochId)
}

func TestRequestThresholdSignature_RejectsCompletedWithoutDisputingDeadline(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	const epochID = uint64(79)
	ctx = ctx.WithBlockHeight(1_000)

	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, buildSigningData(epochID, 20))
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing disputing deadline metadata")
}

func TestRequestThresholdSignature_RejectsCompletedWhenFallbackDisabled(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.CompletedFallbackBlocks = 0
	require.NoError(t, k.SetParams(ctx, params))

	const epochID = uint64(80)
	const disputingDeadlineBlock = int64(100)
	ctx = ctx.WithBlockHeight(disputingDeadlineBlock + 1_000)

	err = k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:                     epochID,
		DkgPhase:                    types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey:              []byte{1},
		DisputingPhaseDeadlineBlock: disputingDeadlineBlock,
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, buildSigningData(epochID, 21))
	require.Error(t, err)
	require.Contains(t, err.Error(), "completed fallback is disabled")
}

func TestRequestThresholdSignature_AllowsSignedEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	const epochID = uint64(81)
	signingData := buildSigningData(epochID, 30)
	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, signingData)
	require.NoError(t, err)

	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, request.Status)
	require.Equal(t, epochID, request.CurrentEpochId)
}

func TestRequestThresholdSignature_RejectsNonSignedNonCompletedEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	const epochID = uint64(82)
	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_VERIFYING,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, buildSigningData(epochID, 40))
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not signed")
}

func buildSigningData(epochID uint64, seed byte) types.SigningData {
	return types.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{seed}, 32),
		RequestId:      bytes.Repeat([]byte{seed + 1}, 32),
		Data:           [][]byte{bytes.Repeat([]byte{seed + 2}, 32)},
	}
}
