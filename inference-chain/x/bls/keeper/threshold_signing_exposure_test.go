package keeper

import (
	"testing"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func TestPartialSignatureExposureSurvivesReset(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	requestID := []byte("exposed-request")
	exposed, err := k.HasExposedPartialSignatures(ctx, requestID)
	require.NoError(t, err)
	require.False(t, exposed)

	require.NoError(t, k.SetThresholdPartialSignature(ctx, requestID, &types.PartialSignature{
		ParticipantAddress: "signer", SlotIndices: []uint32{0}, Signature: []byte{1},
	}))
	require.NoError(t, k.DeleteThresholdPartialSignaturesForRequest(ctx, requestID))
	partials, err := k.ListThresholdPartialSignatures(ctx, requestID)
	require.NoError(t, err)
	require.Empty(t, partials)

	// A new keeper instance must read persisted exposure after all shares are cleared.
	reopened := NewKeeper(k.cdc, k.storeService, k.logger, k.authority)
	exposed, err = reopened.HasExposedPartialSignatures(ctx, requestID)
	require.NoError(t, err)
	require.True(t, exposed)
	exposed, err = reopened.HasExposedPartialSignatures(ctx, []byte("unrelated-request"))
	require.NoError(t, err)
	require.False(t, exposed)
}

func TestPartialSignatureExposureRetainsLegacyShares(t *testing.T) {
	for _, inline := range []bool{false, true} {
		name := "split"
		if inline {
			name = "inline"
		}
		t.Run(name, func(t *testing.T) {
			k, ctx := setupBlsKeeperForRetryTests(t)
			requestID := []byte("legacy-exposed-request")
			partial := types.PartialSignature{ParticipantAddress: "signer", SlotIndices: []uint32{0}, Signature: []byte{1}}
			request := &types.ThresholdSigningRequest{RequestId: requestID}
			if inline {
				request.PartialSignatures = []types.PartialSignature{partial}
			} else {
				value, err := k.cdc.Marshal(&partial)
				require.NoError(t, err)
				k.thresholdPartialSigStore(ctx, requestID).Set(types.ThresholdPartialSigSubKey(partial.ParticipantAddress), value)
			}
			value, err := k.cdc.Marshal(request)
			require.NoError(t, err)
			require.NoError(t, k.storeService.OpenKVStore(ctx).Set(types.ThresholdSigningRequestKey(requestID), value))
			marked, err := k.exposedPartialSignatures.Has(ctx, requestID)
			require.NoError(t, err)
			require.False(t, marked)
			exposed, err := k.HasExposedPartialSignatures(ctx, requestID)
			require.NoError(t, err)
			require.True(t, exposed)

			require.NoError(t, k.DeleteThresholdPartialSignaturesForRequest(ctx, requestID))
			request.PartialSignatures = nil
			require.NoError(t, k.storeThresholdSigningRequest(ctx, request))
			exposed, err = k.HasExposedPartialSignatures(ctx, requestID)
			require.NoError(t, err)
			require.True(t, exposed)
		})
	}
}

func TestPartialSignatureExposureRejectsCorruptMarker(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	requestID := []byte("corrupt-exposure")
	key := append(append([]byte(nil), types.ExposedPartialSignaturesPrefix...), requestID...)
	require.NoError(t, k.storeService.OpenKVStore(ctx).Set(key, []byte{2}))
	_, err := k.HasExposedPartialSignatures(ctx, requestID)
	require.ErrorIs(t, err, collections.ErrEncoding)
}
