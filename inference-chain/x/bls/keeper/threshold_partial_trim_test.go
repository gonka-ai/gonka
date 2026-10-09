package keeper

import (
	"fmt"
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

func trimTestPartial(i int) *types.PartialSignature {
	slots := []uint32{uint32(4 * i), uint32(4*i + 1), uint32(4*i + 2), uint32(4*i + 3)}
	return &types.PartialSignature{
		ParticipantAddress: fmt.Sprintf("gonka1%038d", i),
		SlotIndices:        slots,
		Signature:          make([]byte, 48*len(slots)),
	}
}

func TestThresholdPartialSignature_ValueOmitsSubmitter(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	requestID := []byte("req-trim")
	require.NoError(t, k.storeThresholdSigningRequest(ctx, &types.ThresholdSigningRequest{
		RequestId: requestID,
		Status:    types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
	}))
	in := trimTestPartial(1)
	require.NoError(t, k.SetThresholdPartialSignature(ctx, requestID, in))
	require.Equal(t, "gonka1"+fmt.Sprintf("%038d", 1), in.ParticipantAddress, "caller's value is not mutated")

	raw := k.thresholdPartialSigStore(ctx, requestID).Get(types.ThresholdPartialSigSubKey(in.ParticipantAddress))
	var stored types.PartialSignature
	require.NoError(t, k.cdc.Unmarshal(raw, &stored))
	require.Empty(t, stored.ParticipantAddress)

	got, err := k.GetThresholdPartialSignature(ctx, requestID, in.ParticipantAddress)
	require.NoError(t, err)
	require.Equal(t, in, got)
	list, err := k.ListThresholdPartialSignatures(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, []types.PartialSignature{*in}, list)
	status, err := k.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, []types.PartialSignature{*in}, status.PartialSignatures)
}

func TestThresholdPartialSignature_LegacyValueWithSubmitter(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	requestID := []byte("req-legacy")
	legacy := trimTestPartial(2)
	bz, err := k.cdc.Marshal(legacy)
	require.NoError(t, err)
	k.thresholdPartialSigStore(ctx, requestID).Set(types.ThresholdPartialSigSubKey(legacy.ParticipantAddress), bz)

	got, err := k.GetThresholdPartialSignature(ctx, requestID, legacy.ParticipantAddress)
	require.NoError(t, err)
	require.Equal(t, legacy, got)
	list, err := k.ListThresholdPartialSignatures(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, []types.PartialSignature{*legacy}, list)
}

// Store work of AddPartialSignature for the 11th signer: the status read
// rehydrates the 10 earlier partials, then the new one is written.
func TestThresholdPartialSignature_AddGas(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	requestID := make([]byte, 32)
	require.NoError(t, k.storeThresholdSigningRequest(ctx, &types.ThresholdSigningRequest{
		RequestId: requestID,
		Status:    types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
	}))
	for i := 0; i < 10; i++ {
		require.NoError(t, k.SetThresholdPartialSignature(ctx, requestID, trimTestPartial(i)))
	}

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	status, err := k.GetSigningStatus(gctx, requestID)
	require.NoError(t, err)
	require.Len(t, status.PartialSignatures, 10)
	read := gctx.GasMeter().GasConsumed()
	require.NoError(t, k.SetThresholdPartialSignature(gctx, requestID, trimTestPartial(10)))
	t.Logf("GetSigningStatus (10 partials) gas: %d; SetThresholdPartialSignature gas: %d; total %d",
		read, gctx.GasMeter().GasConsumed()-read, gctx.GasMeter().GasConsumed())
}
