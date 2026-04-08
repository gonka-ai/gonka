package keeper

import (
	"bytes"
	"context"
	"errors"
	"testing"

	corestore "cosmossdk.io/core/store"
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

func TestRequestThresholdSignature_RetryRejectedAfterExpiredWhenMaxAttemptsReached(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(301)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 2)

	signingData := makeSigningDataForRetryTests(epochID, 1)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	initialRequest, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)

	firstDeadlineCtx := ctx.WithBlockHeight(initialRequest.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(firstDeadlineCtx))

	retryRequest, err := k.GetSigningStatus(firstDeadlineCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, retryRequest.Status)
	require.EqualValues(t, 2, retryRequest.Attempt)

	secondDeadlineCtx := firstDeadlineCtx.WithBlockHeight(retryRequest.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(secondDeadlineCtx))

	expiredRequest, err := k.GetSigningStatus(secondDeadlineCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expiredRequest.Status)
	require.EqualValues(t, 2, expiredRequest.Attempt)

	err = k.RequestThresholdSignature(secondDeadlineCtx, signingData)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max signing attempts reached")
}

func TestProcessThresholdSigningDeadlines_AutoRetryKeepsRequestEpochAndStopsAtMaxAttempts(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	initialEpochID := uint64(401)
	activeEpochID := uint64(402)
	setSignedEpochForRetryTests(t, k, ctx, initialEpochID)
	setDealingEpochForRetryTests(t, k, ctx, activeEpochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 3)

	signingData := makeSigningDataForRetryTests(initialEpochID, 21)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	initialRequest, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	initialHash := append([]byte(nil), initialRequest.MessageHash...)

	k.SetActiveEpochID(ctx, activeEpochID)

	retry1Ctx := ctx.WithBlockHeight(initialRequest.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(retry1Ctx))

	retry1Request, err := k.GetSigningStatus(retry1Ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, retry1Request.Status)
	require.EqualValues(t, 2, retry1Request.Attempt)
	require.Equal(t, initialEpochID, retry1Request.CurrentEpochId)
	require.NotEqual(t, retry1Request.DeadlineBlockHeight, initialRequest.DeadlineBlockHeight)
	require.Equal(t, initialHash, retry1Request.MessageHash)
	require.Empty(t, retry1Request.PartialSignatures)

	retry2Ctx := retry1Ctx.WithBlockHeight(retry1Request.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(retry2Ctx))

	retry2Request, err := k.GetSigningStatus(retry2Ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, retry2Request.Status)
	require.EqualValues(t, 3, retry2Request.Attempt)
	require.Equal(t, initialEpochID, retry2Request.CurrentEpochId)

	terminalCtx := retry2Ctx.WithBlockHeight(retry2Request.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(terminalCtx))

	terminalRequest, err := k.GetSigningStatus(terminalCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, terminalRequest.Status)
	require.EqualValues(t, 3, terminalRequest.Attempt)
}

func TestProcessThresholdSigningDeadlines_ProcessesOverdueRequests(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(403)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 1)

	signingData := makeSigningDataForRetryTests(epochID, 22)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)

	overdueCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight + 5)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(overdueCtx))

	expiredRequest, err := k.GetSigningStatus(overdueCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expiredRequest.Status)
}

func TestProcessThresholdSigningDeadlines_RespectsPerBlockLimit(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(404)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 1)

	originalMax := maxExpiredRequestsPerBlock
	maxExpiredRequestsPerBlock = 2
	defer func() {
		maxExpiredRequestsPerBlock = originalMax
	}()

	requestIDs := make([][]byte, 0, 3)
	var deadlineBlockHeight int64
	for i := 0; i < 3; i++ {
		signingData := makeSigningDataForRetryTests(epochID, byte(23+i))
		require.NoError(t, k.RequestThresholdSignature(ctx, signingData))
		requestIDs = append(requestIDs, append([]byte(nil), signingData.RequestId...))

		request, err := k.GetSigningStatus(ctx, signingData.RequestId)
		require.NoError(t, err)
		if i == 0 {
			deadlineBlockHeight = request.DeadlineBlockHeight
		} else {
			require.Equal(t, deadlineBlockHeight, request.DeadlineBlockHeight)
		}
	}

	expiryCtx := ctx.WithBlockHeight(deadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(expiryCtx))

	expiredCount := 0
	collectingCount := 0
	for _, requestID := range requestIDs {
		request, err := k.GetSigningStatus(expiryCtx, requestID)
		require.NoError(t, err)

		switch request.Status {
		case types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED:
			expiredCount++
		case types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES:
			collectingCount++
		default:
			t.Fatalf("unexpected request status after limited pass: %s", request.Status.String())
		}
	}

	require.Equal(t, int(maxExpiredRequestsPerBlock), expiredCount)
	require.Equal(t, len(requestIDs)-int(maxExpiredRequestsPerBlock), collectingCount)

	nextBlockCtx := expiryCtx.WithBlockHeight(expiryCtx.BlockHeight() + 1)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(nextBlockCtx))

	for _, requestID := range requestIDs {
		request, err := k.GetSigningStatus(nextBlockCtx, requestID)
		require.NoError(t, err)
		require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, request.Status)
	}
}

func TestProcessThresholdSigningDeadlines_MalformedKeyCleanedAndDoesNotBypassLimit(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(405)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 1)

	originalMax := maxExpiredRequestsPerBlock
	maxExpiredRequestsPerBlock = 1
	defer func() {
		maxExpiredRequestsPerBlock = originalMax
	}()

	signingData := makeSigningDataForRetryTests(epochID, 26)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)

	kvStore := k.storeService.OpenKVStore(expiryCtx)
	badSuffixKey := []byte{0x00}
	badFullKey := append(append([]byte(nil), types.ExpirationIndexPrefix...), badSuffixKey...)
	require.NoError(t, kvStore.Set(badFullKey, []byte{1}))

	require.NoError(t, k.ProcessThresholdSigningDeadlines(expiryCtx))

	badValue, err := kvStore.Get(badFullKey)
	require.NoError(t, err)
	require.Nil(t, badValue)

	afterFirstPass, err := k.GetSigningStatus(expiryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, afterFirstPass.Status)

	nextBlockCtx := expiryCtx.WithBlockHeight(expiryCtx.BlockHeight() + 1)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(nextBlockCtx))

	afterSecondPass, err := k.GetSigningStatus(nextBlockCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, afterSecondPass.Status)
}

func TestAddPartialSignature_ExpiredRequestAutoRetryAndTerminalExpiry(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(450)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 2)

	signingData := makeSigningDataForRetryTests(epochID, 90)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	initialRequest, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)

	retryCtx := ctx.WithBlockHeight(initialRequest.DeadlineBlockHeight + 1)
	require.NoError(t, k.AddPartialSignature(retryCtx, signingData.RequestId, []uint32{1}, []byte{1}, ""))

	retriedRequest, err := k.GetSigningStatus(retryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, retriedRequest.Status)
	require.EqualValues(t, 2, retriedRequest.Attempt)
	require.Greater(t, retriedRequest.DeadlineBlockHeight, retryCtx.BlockHeight())

	terminalCtx := retryCtx.WithBlockHeight(retriedRequest.DeadlineBlockHeight + 1)
	require.NoError(t, k.AddPartialSignature(terminalCtx, signingData.RequestId, []uint32{1}, []byte{1}, ""))

	terminalRequest, err := k.GetSigningStatus(terminalCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, terminalRequest.Status)
	require.EqualValues(t, 2, terminalRequest.Attempt)
}

func TestMaybeAutoRetryThresholdSigningRequest_DeadlineCollisionKeepsExpirationTracking(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(451)
	setSignedEpochForRetryTests(t, k, ctx, epochID)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Greater(t, params.SigningDeadlineBlocks, int64(0))
	params.MaxSigningAttempts = 2
	require.NoError(t, k.SetParams(ctx, params))

	signingData := makeSigningDataForRetryTests(epochID, 91)
	retryBlockHeight := int64(250)
	collidingDeadline := retryBlockHeight + params.SigningDeadlineBlocks

	request := &types.ThresholdSigningRequest{
		RequestId:           signingData.RequestId,
		CurrentEpochId:      signingData.CurrentEpochId,
		ChainId:             signingData.ChainId,
		Data:                signingData.Data,
		EncodedData:         []byte("encoded"),
		MessageHash:         bytes.Repeat([]byte{0xAB}, 32),
		Status:              types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
		PartialSignatures:   []types.PartialSignature{},
		FinalSignature:      []byte{},
		CreatedBlockHeight:  1,
		DeadlineBlockHeight: collidingDeadline,
		Attempt:             1,
	}
	require.NoError(t, k.storeThresholdSigningRequest(ctx, request))

	kvStore := k.storeService.OpenKVStore(ctx)
	expirationKey := types.ExpirationIndexKey(collidingDeadline, request.RequestId)
	require.NoError(t, kvStore.Set(expirationKey, []byte{1}))

	retryCtx := ctx.WithBlockHeight(retryBlockHeight)
	retried, err := k.maybeAutoRetryThresholdSigningRequest(retryCtx, request, "test deadline collision")
	require.NoError(t, err)
	require.True(t, retried)

	retriedRequest, err := k.GetSigningStatus(retryCtx, request.RequestId)
	require.NoError(t, err)
	require.EqualValues(t, 2, retriedRequest.Attempt)
	require.Equal(t, collidingDeadline, retriedRequest.DeadlineBlockHeight)

	deadlineCtx := retryCtx.WithBlockHeight(collidingDeadline)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(deadlineCtx))

	terminalRequest, err := k.GetSigningStatus(deadlineCtx, request.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, terminalRequest.Status)
}

func TestRequestThresholdSignature_RetryAllowedAfterFailedAndCleansStaleExpirationIndex(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(302)
	setSignedEpochForRetryTests(t, k, ctx, epochID)

	signingData := makeSigningDataForRetryTests(epochID, 2)
	staleDeadline := int64(12345)

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

func TestRequestThresholdSignature_RejectsRetryAfterCancelled(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(305)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 1)

	signingData := makeSigningDataForRetryTests(epochID, 40)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	req, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(req.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(expiryCtx))

	require.NoError(t, k.CancelThresholdSignature(expiryCtx, signingData.RequestId))

	cancelledRequest, err := k.GetSigningStatus(expiryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)

	err = k.RequestThresholdSignature(expiryCtx, signingData)
	require.Error(t, err)
	require.Contains(t, err.Error(), "request_id already exists")
	require.Contains(t, err.Error(), types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED.String())
}

func TestBlsHooksSharedAcrossKeeperCopies(t *testing.T) {
	k, _ := setupBlsKeeperForRetryTests(t)
	kCopy := k

	hook := &retryTestBlsHook{}
	require.NoError(t, kCopy.SetHooks(hook))

	require.NoError(t, k.Hooks().AfterThresholdSigningCompleted(context.Background(), bytes.Repeat([]byte{1}, 32), 1))
	require.True(t, hook.called)
}

func TestProcessThresholdSigningDeadlines_HookCanCloseRetry(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(306)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 1)

	hook := &retryTestBlsHook{closeRetry: true}
	require.NoError(t, k.SetHooks(hook))

	signingData := makeSigningDataForRetryTests(epochID, 50)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	req, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(req.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(expiryCtx))

	cancelledRequest, err := k.GetSigningStatus(expiryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)
}

func TestProcessThresholdSigningDeadlines_HookErrorRollsBackPostProcessSideEffects(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	epochID := uint64(307)
	setSignedEpochForRetryTests(t, k, ctx, epochID)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 1)

	sideEffectKey := []byte("failed_hook_rollback_key")
	sideEffectEvent := "failed_hook_rollback_event"
	hook := &retryTestBlsHook{
		storeService: k.storeService,
		hookKey:      sideEffectKey,
		hookEvent:    sideEffectEvent,
		failedErr:    errors.New("hook failed"),
	}
	require.NoError(t, k.SetHooks(hook))

	signingData := makeSigningDataForRetryTests(epochID, 51)
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	req, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(req.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(expiryCtx))

	updatedRequest, err := k.GetSigningStatus(expiryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, updatedRequest.Status)

	kvStore := k.storeService.OpenKVStore(expiryCtx)
	sideEffectValue, err := kvStore.Get(sideEffectKey)
	require.NoError(t, err)
	require.Nil(t, sideEffectValue)

	foundSideEffectEvent := false
	for _, event := range expiryCtx.EventManager().Events() {
		if event.Type == sideEffectEvent {
			foundSideEffectEvent = true
			break
		}
	}
	require.False(t, foundSideEffectEvent)
}

func TestRunThresholdSigningCompletedPostProcess_ErrorRollsBackSideEffects(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	sideEffectKey := []byte("completed_hook_rollback_key")
	sideEffectEvent := "completed_hook_rollback_event"
	hook := &retryTestBlsHook{
		storeService: k.storeService,
		hookKey:      sideEffectKey,
		hookEvent:    sideEffectEvent,
		completedErr: errors.New("hook failed"),
	}
	require.NoError(t, k.SetHooks(hook))

	err := k.runThresholdSigningCompletedPostProcess(ctx, bytes.Repeat([]byte{9}, 32), 1)
	require.Error(t, err)

	kvStore := k.storeService.OpenKVStore(ctx)
	sideEffectValue, getErr := kvStore.Get(sideEffectKey)
	require.NoError(t, getErr)
	require.Nil(t, sideEffectValue)

	foundSideEffectEvent := false
	for _, event := range ctx.EventManager().Events() {
		if event.Type == sideEffectEvent {
			foundSideEffectEvent = true
			break
		}
	}
	require.False(t, foundSideEffectEvent)
}

func TestProcessCompletedPostProcessRetries_RetriesAndClearsQueue(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	sideEffectKey := []byte("completed_retry_hook_key")
	sideEffectEvent := "completed_retry_hook_event"
	hook := &retryTestBlsHook{
		storeService: k.storeService,
		hookKey:      sideEffectKey,
		hookEvent:    sideEffectEvent,
		completedErr: errors.New("transient failure"),
	}
	require.NoError(t, k.SetHooks(hook))

	requestID := bytes.Repeat([]byte{0x6A}, 32)
	request := &types.ThresholdSigningRequest{
		RequestId:      requestID,
		CurrentEpochId: 777,
		Status:         types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED,
	}
	require.NoError(t, k.storeThresholdSigningRequest(ctx, request))

	kvStore := k.storeService.OpenKVStore(ctx)
	k.enqueueCompletedPostProcessRetry(ctx, requestID)

	queueValue, err := kvStore.Get(types.CompletedPostProcessRetryKey(requestID))
	require.NoError(t, err)
	require.NotNil(t, queueValue)

	require.NoError(t, k.ProcessCompletedPostProcessRetries(ctx))

	queueValue, err = kvStore.Get(types.CompletedPostProcessRetryKey(requestID))
	require.NoError(t, err)
	require.NotNil(t, queueValue)

	sideEffectValue, err := kvStore.Get(sideEffectKey)
	require.NoError(t, err)
	require.Nil(t, sideEffectValue)

	hook.completedErr = nil

	require.NoError(t, k.ProcessCompletedPostProcessRetries(ctx))

	queueValue, err = kvStore.Get(types.CompletedPostProcessRetryKey(requestID))
	require.NoError(t, err)
	require.Nil(t, queueValue)

	sideEffectValue, err = kvStore.Get(sideEffectKey)
	require.NoError(t, err)
	require.NotNil(t, sideEffectValue)

	foundSideEffectEvent := false
	for _, event := range ctx.EventManager().Events() {
		if event.Type == sideEffectEvent {
			foundSideEffectEvent = true
			break
		}
	}
	require.True(t, foundSideEffectEvent)
}

func TestProcessCompletedPostProcessRetries_RemovesMissingRequestQueueEntry(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	requestID := bytes.Repeat([]byte{0x7B}, 32)
	kvStore := k.storeService.OpenKVStore(ctx)
	k.enqueueCompletedPostProcessRetry(ctx, requestID)

	queueValue, err := kvStore.Get(types.CompletedPostProcessRetryKey(requestID))
	require.NoError(t, err)
	require.NotNil(t, queueValue)

	require.NoError(t, k.ProcessCompletedPostProcessRetries(ctx))

	queueValue, err = kvStore.Get(types.CompletedPostProcessRetryKey(requestID))
	require.NoError(t, err)
	require.Nil(t, queueValue)
}

type retryTestBlsHook struct {
	called       bool
	closeRetry   bool
	storeService corestore.KVStoreService
	hookKey      []byte
	hookEvent    string
	completedErr error
	failedErr    error
}

func (h *retryTestBlsHook) writeSideEffects(ctx context.Context) error {
	if h.storeService != nil && len(h.hookKey) > 0 {
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		kvStore := h.storeService.OpenKVStore(sdkCtx)
		if err := kvStore.Set(h.hookKey, []byte{1}); err != nil {
			return err
		}
	}
	if h.hookEvent != "" {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(h.hookEvent))
	}
	return nil
}

func (h *retryTestBlsHook) AfterThresholdSigningCompleted(ctx context.Context, _ []byte, _ uint64) error {
	h.called = true
	if err := h.writeSideEffects(ctx); err != nil {
		return err
	}
	return h.completedErr
}

func (h *retryTestBlsHook) AfterThresholdSigningFailed(ctx context.Context, _ []byte, _ uint64, _ string) (bool, error) {
	if err := h.writeSideEffects(ctx); err != nil {
		return false, err
	}
	return h.closeRetry, h.failedErr
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

func setDealingEpochForRetryTests(t *testing.T, k Keeper, ctx sdk.Context, epochID uint64) {
	t.Helper()

	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_DEALING,
		GroupPublicKey: []byte{},
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

func setMaxSigningAttemptsForRetryTests(t *testing.T, k Keeper, ctx sdk.Context, maxAttempts uint32) {
	t.Helper()

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.MaxSigningAttempts = maxAttempts
	require.NoError(t, k.SetParams(ctx, params))
}
