package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
	"golang.org/x/crypto/sha3"
)

func setupMsgServerThresholdSigning(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context) {
	k, ctx := keepertest.BlsKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func setCompletedEpoch(t testing.TB, k keeper.Keeper, ctx sdk.Context, epochID uint64) {
	t.Helper()
	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: []byte{1, 2, 3},
	})
	require.NoError(t, err)
}

func TestCurrentSigningEpochID_SetGet(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	epochID, found := k.GetCurrentSigningEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), epochID)

	k.SetCurrentSigningEpochID(ctx, 7)

	epochID, found = k.GetCurrentSigningEpochID(ctx)
	require.True(t, found)
	require.Equal(t, uint64(7), epochID)
}

func TestRequestThresholdSignature_RejectsWhenCurrentSigningEpochUnset(t *testing.T) {
	k, ms, sdkCtx := setupMsgServerThresholdSigning(t)
	setCompletedEpoch(t, k, sdkCtx, 1)

	msg := &types.MsgRequestThresholdSignature{
		Creator:        "gonka1externaluser",
		CurrentEpochId: 1,
		ChainId:        []byte("chain"),
		RequestId:      []byte("req-unset"),
		Data:           [][]byte{[]byte("payload")},
	}

	_, err := ms.RequestThresholdSignature(sdkCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current signing epoch is not set")
}

func TestRequestThresholdSignature_RejectsStaleEpoch(t *testing.T) {
	k, ms, sdkCtx := setupMsgServerThresholdSigning(t)
	setCompletedEpoch(t, k, sdkCtx, 1)
	k.SetCurrentSigningEpochID(sdkCtx, 2)

	msg := &types.MsgRequestThresholdSignature{
		Creator:        "gonka1externaluser",
		CurrentEpochId: 1,
		ChainId:        []byte("chain"),
		RequestId:      []byte("req-stale"),
		Data:           [][]byte{[]byte("payload")},
	}

	_, err := ms.RequestThresholdSignature(sdkCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current_epoch_id mismatch")
	assert.Contains(t, err.Error(), "expected 2, got 1")
}

func TestRequestThresholdSignature_AcceptsCurrentEpoch(t *testing.T) {
	k, ms, sdkCtx := setupMsgServerThresholdSigning(t)
	// Signing is allowed immediately for DKG_PHASE_SIGNED. DKG_PHASE_COMPLETED without
	// DisputingPhaseDeadlineBlock / fallback timing is rejected (see threshold_signing_test.go).
	require.NoError(t, k.SetEpochBLSData(sdkCtx, types.EpochBLSData{
		EpochId:        2,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1, 2, 3},
	}))
	k.SetCurrentSigningEpochID(sdkCtx, 2)

	msg := &types.MsgRequestThresholdSignature{
		Creator:        "gonka1externaluser",
		CurrentEpochId: 2,
		ChainId:        []byte("chain"),
		RequestId:      []byte("req-current"),
		Data:           [][]byte{[]byte("payload")},
	}

	_, err := ms.RequestThresholdSignature(sdkCtx, msg)
	require.NoError(t, err)

	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(msg.Creator))
	hash.Write(msg.RequestId)
	namespacedRequestId := hash.Sum(nil)

	stored, err := k.GetSigningStatus(sdkCtx, namespacedRequestId)
	require.NoError(t, err)
	require.Equal(t, uint64(2), stored.CurrentEpochId)
}
