package keeper

import (
	"bytes"
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

func TestRequestThresholdSignature_RetryAllowedAfterExpired(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(301)
	setSignedEpochForRetryTests(t, k, ctx, epochID)

	signingData := makeSigningDataForRetryTests(epochID, 1)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	initialRequest, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)

	// Expire exactly at deadline block so expiration index processing picks it up
	expiryCtx := ctx.WithBlockHeight(initialRequest.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(expiryCtx))

	expiredRequest, err := k.GetSigningStatus(expiryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expiredRequest.Status)

	// Retry with the same request_id should now be allowed
	require.NoError(t, k.RequestThresholdSignature(expiryCtx, signingData))

	retriedRequest, err := k.GetSigningStatus(expiryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, retriedRequest.Status)
	require.Equal(t, expiryCtx.BlockHeight(), retriedRequest.CreatedBlockHeight)
	require.Greater(t, retriedRequest.DeadlineBlockHeight, retriedRequest.CreatedBlockHeight)
	require.Empty(t, retriedRequest.PartialSignatures)
	require.Empty(t, retriedRequest.FinalSignature)
}

func TestRequestThresholdSignature_RetryAllowedAfterFailedAndCleansStaleExpirationIndex(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(302)
	setSignedEpochForRetryTests(t, k, ctx, epochID)

	signingData := makeSigningDataForRetryTests(epochID, 2)
	staleDeadline := int64(12345)

	// Seed an existing FAILED request and a stale expiration index entry
	failedRequest := &types.ThresholdSigningRequest{
		RequestId:           signingData.RequestId,
		CurrentEpochId:      signingData.CurrentEpochId,
		ChainId:             signingData.ChainId,
		Data:                signingData.Data,
		EncodedData:         []byte("old-encoded"),
		MessageHash:         bytes.Repeat([]byte{9}, 32),
		Status:              types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED,
		PartialSignatures:   []types.PartialSignature{{ParticipantAddress: "p1"}},
		FinalSignature:      []byte{7, 7, 7},
		CreatedBlockHeight:  10,
		DeadlineBlockHeight: staleDeadline,
	}
	require.NoError(t, k.storeThresholdSigningRequest(ctx, failedRequest))

	kvStore := k.storeService.OpenKVStore(ctx)
	staleExpirationKey := types.ExpirationIndexKey(staleDeadline, signingData.RequestId)
	require.NoError(t, kvStore.Set(staleExpirationKey, []byte{1}))

	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	// Stale index entry from the failed attempt must be removed
	staleValue, err := kvStore.Get(staleExpirationKey)
	require.NoError(t, err)
	require.Nil(t, staleValue)

	retriedRequest, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, retriedRequest.Status)
	require.Equal(t, ctx.BlockHeight(), retriedRequest.CreatedBlockHeight)
	require.Empty(t, retriedRequest.PartialSignatures)
	require.Empty(t, retriedRequest.FinalSignature)
}

func TestRequestThresholdSignature_RetryRejectsPayloadMismatch(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(304)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	retryEpochID := epochID + 1
	setSignedEpochForRetryTests(t, k, ctx, retryEpochID)

	signingData := makeSigningDataForRetryTests(epochID, 3)
	failedRequest := &types.ThresholdSigningRequest{
		RequestId:           signingData.RequestId,
		CurrentEpochId:      signingData.CurrentEpochId,
		ChainId:             signingData.ChainId,
		Data:                signingData.Data,
		EncodedData:         []byte("old-encoded"),
		MessageHash:         bytes.Repeat([]byte{5}, 32),
		Status:              types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED,
		PartialSignatures:   []types.PartialSignature{},
		FinalSignature:      []byte{},
		CreatedBlockHeight:  10,
		DeadlineBlockHeight: 20,
	}
	require.NoError(t, k.storeThresholdSigningRequest(ctx, failedRequest))

	testCases := []struct {
		name       string
		chainID    []byte
		dataFields [][]byte
	}{
		{
			name:       "chain id mismatch",
			chainID:    bytes.Repeat([]byte{0xAA}, 32),
			dataFields: signingData.Data,
		},
		{
			name:       "data mismatch",
			chainID:    signingData.ChainId,
			dataFields: [][]byte{bytes.Repeat([]byte{0xBB}, 32)},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := k.RequestThresholdSignature(ctx, types.SigningData{
				CurrentEpochId: retryEpochID,
				ChainId:        tc.chainID,
				RequestId:      signingData.RequestId,
				Data:           tc.dataFields,
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "payload mismatch")

			stored, getErr := k.GetSigningStatus(ctx, signingData.RequestId)
			require.NoError(t, getErr)
			require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED, stored.Status)
			require.Equal(t, signingData.ChainId, stored.ChainId)
			require.Equal(t, signingData.Data, stored.Data)
			require.Equal(t, int64(10), stored.CreatedBlockHeight)
			require.Equal(t, int64(20), stored.DeadlineBlockHeight)
		})
	}
}

func TestRequestThresholdSignature_RejectsDuplicateRequestIDForActiveAndCompleted(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(303)
	setSignedEpochForRetryTests(t, k, ctx, epochID)

	testCases := []struct {
		name   string
		status types.ThresholdSigningStatus
	}{
		{
			name:   "collecting signatures",
			status: types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
		},
		{
			name:   "completed",
			status: types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED,
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			signingData := makeSigningDataForRetryTests(epochID, byte(10+i))
			existing := &types.ThresholdSigningRequest{
				RequestId:           signingData.RequestId,
				CurrentEpochId:      signingData.CurrentEpochId,
				ChainId:             signingData.ChainId,
				Data:                signingData.Data,
				EncodedData:         []byte("existing"),
				MessageHash:         bytes.Repeat([]byte{8}, 32),
				Status:              tc.status,
				PartialSignatures:   []types.PartialSignature{},
				FinalSignature:      []byte{},
				CreatedBlockHeight:  1,
				DeadlineBlockHeight: 20,
			}
			require.NoError(t, k.storeThresholdSigningRequest(ctx, existing))

			err := k.RequestThresholdSignature(ctx, signingData)
			require.Error(t, err)
			require.Contains(t, err.Error(), "request_id already exists")
			require.Contains(t, err.Error(), tc.status.String())
		})
	}
}

func setSignedEpochForRetryTests(t *testing.T, k Keeper, ctx sdk.Context, epochID uint64) {
	t.Helper()

	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)
}

func makeSigningDataForRetryTests(epochID uint64, marker byte) types.SigningData {
	return types.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{marker}, 32),
		RequestId:      bytes.Repeat([]byte{marker + 1}, 32),
		Data:           [][]byte{bytes.Repeat([]byte{marker + 2}, 32)},
	}
}

func setupBlsKeeperForRetryTests(t *testing.T) (Keeper, sdk.Context) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	k := NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		log.NewNopLogger(),
		authority.String(),
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	return k, ctx
}
