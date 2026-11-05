package keeper

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// seedDKGEpochForPruningTests writes a SIGNED epoch with n participants and
// one dealer part, verification submission and complaint per participant.
func seedDKGEpochForPruningTests(t *testing.T, k Keeper, ctx sdk.Context, epochID uint64, n int) {
	t.Helper()
	participants := make([]types.BLSParticipantInfo, n)
	for i := range participants {
		participants[i] = types.BLSParticipantInfo{Address: "addr", SlotStartIndex: uint32(i), SlotEndIndex: uint32(i)}
	}
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{byte(epochID)},
		Participants:   participants,
	}))
	for i := 0; i < n; i++ {
		require.NoError(t, k.SetDealerPart(ctx, epochID, uint32(i), &types.DealerPartStorage{
			DealerAddress: "addr",
			Commitments:   [][]byte{{byte(i)}},
		}))
		require.NoError(t, k.SetVerificationSubmission(ctx, epochID, uint32(i), &types.VerificationVectorSubmission{
			DealerValidity: []bool{true},
		}))
		require.NoError(t, k.SetDealerComplaint(ctx, epochID, &types.DealerComplaint{
			DealerIndex:     uint32(i),
			ComplainerIndex: uint32((i + 1) % n),
		}))
	}
}

func requireDKGSubKeys(t *testing.T, k Keeper, ctx sdk.Context, epochID uint64, present bool) {
	t.Helper()
	dp, err := k.GetDealerPart(ctx, epochID, 0)
	require.NoError(t, err)
	vs, err := k.GetVerificationSubmission(ctx, epochID, 0)
	require.NoError(t, err)
	complaints, err := k.ListDealerComplaintsForEpoch(ctx, epochID)
	require.NoError(t, err)
	if present {
		require.NotNil(t, dp, "epoch %d dealer part", epochID)
		require.NotNil(t, vs, "epoch %d verification submission", epochID)
		require.NotEmpty(t, complaints, "epoch %d complaints", epochID)
	} else {
		require.Nil(t, dp, "epoch %d dealer part", epochID)
		require.Nil(t, vs, "epoch %d verification submission", epochID)
		require.Empty(t, complaints, "epoch %d complaints", epochID)
	}
	// The base record survives either way: old signatures verify against it.
	base, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Equal(t, []byte{byte(epochID)}, base.GroupPublicKey)
	require.Len(t, base.Participants, 3)
}

func TestPruneDKGSubKeys_PrunesInOrderAndKeepsLastTwoEpochs(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	for e := uint64(10); e <= 14; e++ {
		seedDKGEpochForPruningTests(t, k, ctx, e, 3)
	}

	// One epoch per block: 10, 11, 12 become prunable (latest DKG is 14).
	for i := 0; i < 10; i++ {
		require.NoError(t, k.PruneDKGSubKeys(ctx))
	}
	for e := uint64(10); e <= 12; e++ {
		requireDKGSubKeys(t, k, ctx, e, false)
	}
	requireDKGSubKeys(t, k, ctx, 13, true)
	requireDKGSubKeys(t, k, ctx, 14, true)
	pruned, found := k.getDKGSubKeysPrunedEpoch(ctx)
	require.True(t, found)
	require.EqualValues(t, 12, pruned)

	// The next DKG makes epoch 13 prunable.
	seedDKGEpochForPruningTests(t, k, ctx, 15, 3)
	require.NoError(t, k.PruneDKGSubKeys(ctx))
	requireDKGSubKeys(t, k, ctx, 13, false)
	requireDKGSubKeys(t, k, ctx, 14, true)
}

func TestPruneDKGSubKeys_OneEpochPerBlock(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	for e := uint64(1); e <= 5; e++ {
		seedDKGEpochForPruningTests(t, k, ctx, e, 3)
	}
	require.NoError(t, k.PruneDKGSubKeys(ctx))
	requireDKGSubKeys(t, k, ctx, 1, false)
	requireDKGSubKeys(t, k, ctx, 2, true)
}

// A pending signing request for epoch E holds the cursor at E: dAPI rebuilds
// its slot shares from E's dealer parts to sign it after a restart.
func TestPruneDKGSubKeys_PendingSigningRequestHoldsEpoch(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	for e := uint64(20); e <= 23; e++ {
		seedDKGEpochForPruningTests(t, k, ctx, e, 3)
	}
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 1)

	signingData := makeSigningDataForRetryTests(21, 7)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))
	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		require.NoError(t, k.PruneDKGSubKeys(ctx))
	}
	requireDKGSubKeys(t, k, ctx, 20, false)
	requireDKGSubKeys(t, k, ctx, 21, true)

	// Past the deadline the request expires and leaves the expiration index;
	// epoch 21 becomes prunable.
	deadlineCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(deadlineCtx))
	expired, err := k.GetSigningStatus(deadlineCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expired.Status)

	require.NoError(t, k.PruneDKGSubKeys(deadlineCtx))
	requireDKGSubKeys(t, k, ctx, 21, false)
	requireDKGSubKeys(t, k, ctx, 22, true)
}

func TestPruneDKGSubKeys_NoOpBelowThreeEpochs(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	require.NoError(t, k.PruneDKGSubKeys(ctx))
	seedDKGEpochForPruningTests(t, k, ctx, 1, 3)
	seedDKGEpochForPruningTests(t, k, ctx, 2, 3)
	require.NoError(t, k.PruneDKGSubKeys(ctx))
	requireDKGSubKeys(t, k, ctx, 1, true)
	_, found := k.getDKGSubKeysPrunedEpoch(ctx)
	require.False(t, found)
}
