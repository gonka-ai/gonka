package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/math"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

type reissueHookSpy struct {
	completed, failed int
	completedErr      error
}

func (h *reissueHookSpy) AfterThresholdSigningCompleted(context.Context, []byte, uint64) error {
	h.completed++
	return h.completedErr
}

func (h *reissueHookSpy) AfterThresholdSigningFailed(context.Context, []byte, uint64, string) (bool, error) {
	h.failed++
	return true, nil // models an installed refund hook that would cancel the request
}

type reissueFixture struct {
	k        Keeper
	ctx      sdk.Context
	data     types.SigningData
	original *types.ThresholdSigningRequest
	actor    string
	hooks    *reissueHookSpy
}

func reissuePublicKey(scalar uint64) []byte {
	_, _, _, generator := bls12381.Generators()
	var point bls12381.G2Affine
	point.ScalarMultiplication(&generator, new(big.Int).SetUint64(scalar))
	encoded := point.Bytes()
	return encoded[:]
}

func reissuePartialSignature(hash []byte, scalar uint64, slots int) ([]byte, error) {
	if len(hash) != 32 || slots != 1 {
		return nil, fmt.Errorf("expected a 32-byte message hash and one signing slot")
	}
	var u fp.Element
	u.SetBytes(hash)
	mapped := bls12381.MapToCurve1(&u)
	hash_to_curve.G1Isogeny(&mapped.X, &mapped.Y)
	var message, signature bls12381.G1Affine
	message.ClearCofactor(&mapped)
	signature.ScalarMultiplication(&message, new(big.Int).SetUint64(scalar))
	encoded := signature.Bytes()
	return encoded[:], nil
}

func newReissueFixture(t *testing.T) reissueFixture {
	t.Helper()
	k, ctx := setupBlsKeeperForRetryTests(t)
	actor := authtypes.NewModuleAddress("reissue-signer").String()
	setReissueEpoch(t, k, ctx, 500, 7, actor)
	setReissueEpoch(t, k, ctx, 502, 11, actor)
	setReissueEpoch(t, k, ctx, 503, 13, actor)
	setReissueEpoch(t, k, ctx, 504, 17, actor)
	setMaxSigningAttemptsForRetryTests(t, k, ctx, 3)
	hooks := &reissueHookSpy{}
	require.NoError(t, k.SetHooks(hooks))
	data := makeSigningDataForRetryTests(500, 0xA3)
	data.ReissueSigners = []string{actor}
	require.NoError(t, k.RequestThresholdSignature(ctx, data))
	completeReissueRequest(t, k, ctx, data.RequestId, actor, 7)
	original, err := k.GetSigningStatus(ctx, data.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, original.Status)
	require.Equal(t, uint64(500), original.LastCompletedEpochId)
	require.Equal(t, original.FinalSignature, original.LastCompletedSignature)
	require.True(t, k.verifyFinalSignatureBlst(original.FinalSignature, original.MessageHash, reissuePublicKey(7)))
	return reissueFixture{k, ctx.WithEventManager(sdk.NewEventManager()), data, original, actor, hooks}
}

func setReissueEpoch(t *testing.T, k Keeper, ctx sdk.Context, epoch, scalar uint64, actor string) {
	t.Helper()
	key := reissuePublicKey(scalar)
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId: epoch, DkgPhase: types.DKGPhase_DKG_PHASE_SIGNED,
		ITotalSlots: 1, TSlotsDegree: 0, GroupPublicKey: key,
		SlotPublicKeys: [][]byte{key},
		Participants: []types.BLSParticipantInfo{{
			Address: actor, PercentageWeight: math.LegacyOneDec(),
			SlotStartIndex: 0, SlotEndIndex: 0,
		}},
	}))
}

func completeReissueRequest(t *testing.T, k Keeper, ctx sdk.Context, id []byte, actor string, scalar uint64) {
	t.Helper()
	request, err := k.GetSigningStatus(ctx, id)
	require.NoError(t, err)
	signature, err := reissuePartialSignature(request.MessageHash, scalar, 1)
	require.NoError(t, err)
	require.NoError(t, k.AddPartialSignature(ctx, id, []uint32{0}, signature, actor))
}

func countReissueEvents(ctx sdk.Context, kind string) int {
	count := 0
	for _, event := range ctx.EventManager().Events() {
		if event.Type == kind {
			count++
		}
	}
	return count
}

func TestReissueThresholdSignature_CompletesUnderNewEpochWithoutChangingPayload(t *testing.T) {
	f := newReissueFixture(t)
	store := f.k.storeService.OpenKVStore(f.ctx)
	// A completion cleanup may still be pending when the user reissues.
	require.NoError(t, store.Set(types.CompletedPostProcessRetryKey(f.data.RequestId), []byte{}))
	require.NoError(t, f.k.ReissueThresholdSignature(f.ctx, f.data.RequestId, 502))
	request, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, request.Status)
	require.Equal(t, f.original.RequestId, request.RequestId)
	require.Equal(t, f.original.ChainId, request.ChainId)
	require.Equal(t, f.original.Data, request.Data)
	require.Equal(t, f.original.ReissueSigners, request.ReissueSigners)
	require.Equal(t, uint64(502), request.CurrentEpochId)
	require.Equal(t, uint64(502), request.LastReissueEpochId)
	require.Equal(t, uint64(1), request.ReissueCount)
	require.EqualValues(t, 1, request.Attempt)
	require.Empty(t, request.FinalSignature)
	require.Empty(t, request.PartialSignatures)
	require.Equal(t, f.original.FinalSignature, request.LastCompletedSignature)
	require.Equal(t, uint64(500), request.LastCompletedEpochId)
	require.NotEqual(t, f.original.EncodedData, request.EncodedData)
	require.NotEqual(t, f.original.MessageHash, request.MessageHash)
	require.Equal(t, 1, countReissueEvents(f.ctx, "inference.bls.EventThresholdSigningRequested"))
	queued, err := store.Has(types.CompletedPostProcessRetryKey(f.data.RequestId))
	require.NoError(t, err)
	require.False(t, queued)
	require.Error(t, f.k.CancelThresholdSignature(f.ctx, f.data.RequestId))
	require.Error(t, f.k.RequestThresholdSignature(f.ctx, f.data))

	oldPartial, err := reissuePartialSignature(f.original.MessageHash, 7, 1)
	require.NoError(t, err)
	require.ErrorContains(t, f.k.AddPartialSignature(f.ctx, f.data.RequestId, []uint32{0}, oldPartial, f.actor), "verification failed")
	completeReissueRequest(t, f.k, f.ctx, f.data.RequestId, f.actor, 11)
	completed, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, completed.Status)
	require.Equal(t, uint64(502), completed.LastCompletedEpochId)
	require.Equal(t, completed.FinalSignature, completed.LastCompletedSignature)
	require.True(t, f.k.verifyFinalSignatureBlst(completed.FinalSignature, completed.MessageHash, reissuePublicKey(11)))
	require.Equal(t, 2, f.hooks.completed)
	require.Zero(t, f.hooks.failed)
	require.Error(t, f.k.ReissueThresholdSignature(f.ctx, f.data.RequestId, 503))
	require.NoError(t, f.k.ReissueThresholdSignature(f.ctx, f.data.RequestId, 504))
	completeReissueRequest(t, f.k, f.ctx, f.data.RequestId, f.actor, 17)
	completed, err = f.k.GetSigningStatus(f.ctx, f.data.RequestId)
	require.NoError(t, err)
	require.Equal(t, uint64(2), completed.ReissueCount)
	require.Equal(t, uint64(504), completed.LastCompletedEpochId)
}

func TestReissueThresholdSignature_FailureRestoresSignatureWithoutRefund(t *testing.T) {
	for _, trigger := range []string{"deadline", "late partial", "aggregation"} {
		t.Run(trigger, func(t *testing.T) {
			f := newReissueFixture(t)
			require.NoError(t, f.k.ReissueThresholdSignature(f.ctx, f.data.RequestId, 502))
			request, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
			require.NoError(t, err)
			// Persist an incomplete/bad attempt to prove restoration removes its
			// sub-keys rather than mixing them with the next epoch's signatures.
			badPartial := types.PartialSignature{ParticipantAddress: f.actor, SlotIndices: []uint32{0}, Signature: []byte{1}}
			require.NoError(t, f.k.SetThresholdPartialSignature(f.ctx, f.data.RequestId, &badPartial))
			failureCtx := f.ctx.WithBlockHeight(request.DeadlineBlockHeight + 1).WithEventManager(sdk.NewEventManager())
			switch trigger {
			case "deadline":
				require.NoError(t, f.k.ProcessThresholdSigningDeadlines(failureCtx))
			case "late partial":
				require.NoError(t, f.k.AddPartialSignature(failureCtx, f.data.RequestId, []uint32{0}, []byte{1}, f.actor))
			case "aggregation":
				request.PartialSignatures = []types.PartialSignature{badPartial}
				epoch, err := f.k.GetEpochBLSData(failureCtx, 502)
				require.NoError(t, err)
				require.NoError(t, f.k.checkThresholdAndAggregate(failureCtx, request, &epoch))
			}
			restored, err := f.k.GetSigningStatus(failureCtx, f.data.RequestId)
			require.NoError(t, err)
			require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, restored.Status)
			require.Equal(t, f.original.CurrentEpochId, restored.CurrentEpochId)
			require.Equal(t, f.original.FinalSignature, restored.FinalSignature)
			require.Equal(t, f.original.EncodedData, restored.EncodedData)
			require.Equal(t, f.original.MessageHash, restored.MessageHash)
			require.Empty(t, restored.PartialSignatures)
			require.Equal(t, uint64(502), restored.LastReissueEpochId)
			require.Equal(t, 2, f.hooks.completed, "restoration retries the earlier signature's cleanup")
			require.Zero(t, f.hooks.failed, "an already redeemable request must never reach refund hooks")
			require.Equal(t, 1, countReissueEvents(failureCtx, "threshold_signing_reissue_restored"))
			require.Zero(t, countReissueEvents(failureCtx, "inference.bls.EventThresholdSigningFailed"))
			require.Error(t, f.k.ReissueThresholdSignature(failureCtx, f.data.RequestId, 502), "same-epoch spam must remain blocked after failure")
			require.NoError(t, f.k.ReissueThresholdSignature(failureCtx, f.data.RequestId, 503))
			completeReissueRequest(t, f.k, failureCtx, f.data.RequestId, f.actor, 13)
			require.Zero(t, f.hooks.failed)
		})
	}
}

func TestReissueThresholdSignature_RejectsInvalidRequestsWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"legacy", "collecting", "bad signature", "too early", "older epoch", "unknown epoch", "unsigned epoch", "missing key", "same attempt", "counter overflow"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReissueFixture(t)
			request := *f.original
			epoch := uint64(502)
			switch scenario {
			case "legacy":
				request.ReissueSigners = nil
			case "collecting":
				request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES
			case "bad signature":
				request.FinalSignature = bytes.Repeat([]byte{0}, 48)
			case "too early":
				epoch = 501
			case "older epoch":
				epoch = 499
			case "unknown epoch":
				epoch = 999
			case "unsigned epoch":
				e, err := f.k.GetEpochBLSData(f.ctx, epoch)
				require.NoError(t, err)
				e.DkgPhase = types.DKGPhase_DKG_PHASE_DEALING
				require.NoError(t, f.k.SetEpochBLSData(f.ctx, e))
			case "missing key":
				e, err := f.k.GetEpochBLSData(f.ctx, epoch)
				require.NoError(t, err)
				e.GroupPublicKey = nil
				require.NoError(t, f.k.SetEpochBLSData(f.ctx, e))
			case "same attempt":
				request.LastReissueEpochId = epoch
			case "counter overflow":
				request.ReissueCount = ^uint64(0)
			}
			require.NoError(t, f.k.storeThresholdSigningRequest(f.ctx, &request))
			before, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
			require.NoError(t, err)
			require.Error(t, f.k.ReissueThresholdSignature(f.ctx, f.data.RequestId, epoch))
			after, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Empty(t, f.ctx.EventManager().Events())
		})
	}
}

func TestRequestThresholdSignature_RetryCannotChangeReissueAuthority(t *testing.T) {
	for _, eligible := range []bool{false, true} {
		k, ctx := setupBlsKeeperForRetryTests(t)
		setSignedEpochForRetryTests(t, k, ctx, 500)
		setMaxSigningAttemptsForRetryTests(t, k, ctx, 3)
		data := makeSigningDataForRetryTests(500, 0xA4)
		if eligible {
			data.ReissueSigners = []string{"original-owner"}
		}
		require.NoError(t, k.RequestThresholdSignature(ctx, data))
		request, err := k.GetSigningStatus(ctx, data.RequestId)
		require.NoError(t, err)
		request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED
		require.NoError(t, k.storeThresholdSigningRequest(ctx, request))
		data.ReissueSigners = []string{"different-owner"}
		require.NoError(t, k.RequestThresholdSignature(ctx, data))
		retried, err := k.GetSigningStatus(ctx, data.RequestId)
		require.NoError(t, err)
		require.Equal(t, request.ReissueSigners, retried.ReissueSigners)
	}
}

func TestCompletedSignatureCannotUseGenericRetryOrCancellationEvenWithFailedStatus(t *testing.T) {
	f := newReissueFixture(t)
	request := *f.original
	request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED
	require.NoError(t, f.k.storeThresholdSigningRequest(f.ctx, &request))
	require.ErrorContains(t, f.k.RequestThresholdSignature(f.ctx, f.data), "dedicated reissue")
	require.ErrorContains(t, f.k.CancelThresholdSignature(f.ctx, f.data.RequestId), "completed signature")
	current, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
	require.NoError(t, err)
	require.Equal(t, request.FinalSignature, current.FinalSignature)
	require.Equal(t, request.LastCompletedSignature, current.LastCompletedSignature)
}

type reissueFailingSetService struct {
	corestore.KVStoreService
	key []byte
}
type reissueFailingSetStore struct {
	corestore.KVStore
	key []byte
}

func (s reissueFailingSetService) OpenKVStore(ctx context.Context) corestore.KVStore {
	return reissueFailingSetStore{s.KVStoreService.OpenKVStore(ctx), s.key}
}
func (s reissueFailingSetStore) Set(key, value []byte) error {
	if bytes.Equal(key, s.key) {
		return errors.New("injected reissue store failure")
	}
	return s.KVStore.Set(key, value)
}

func TestReissueThresholdSignature_StoreFailureRollsBackCleanupAndRequest(t *testing.T) {
	f := newReissueFixture(t)
	params, err := f.k.GetParams(f.ctx)
	require.NoError(t, err)
	store := f.k.storeService.OpenKVStore(f.ctx)
	queueKey := types.CompletedPostProcessRetryKey(f.data.RequestId)
	require.NoError(t, store.Set(queueKey, []byte{0, 0, 0, 3}))
	f.k.storeService = reissueFailingSetService{f.k.storeService, types.ExpirationIndexKey(f.ctx.BlockHeight()+params.SigningDeadlineBlocks, f.data.RequestId)}
	require.ErrorContains(t, f.k.ReissueThresholdSignature(f.ctx, f.data.RequestId, 502), "injected reissue store failure")
	current, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
	require.NoError(t, err)
	require.Equal(t, f.original, current)
	queueValue, err := store.Get(queueKey)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0, 0, 3}, queueValue)
	require.Empty(t, f.ctx.EventManager().Events())
}

func TestReissueThresholdSignature_FailureKeepsEarlierCompletionCleanupRetry(t *testing.T) {
	f := newReissueFixture(t)
	store := f.k.storeService.OpenKVStore(f.ctx)
	queueKey := types.CompletedPostProcessRetryKey(f.data.RequestId)
	require.NoError(t, store.Set(queueKey, []byte{}))
	f.hooks.completedErr = errors.New("cleanup still unavailable")
	require.NoError(t, f.k.ReissueThresholdSignature(f.ctx, f.data.RequestId, 502))
	request, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
	require.NoError(t, err)
	queued, err := store.Has(queueKey)
	require.NoError(t, err)
	require.False(t, queued)
	require.NoError(t, f.k.ProcessThresholdSigningDeadlines(f.ctx.WithBlockHeight(request.DeadlineBlockHeight)))
	queued, err = store.Has(queueKey)
	require.NoError(t, err)
	require.True(t, queued, "the previous signature's failed cleanup must survive restoration")
	restored, err := f.k.GetSigningStatus(f.ctx, f.data.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, restored.Status)
	require.Equal(t, f.original.FinalSignature, restored.FinalSignature)
	require.Zero(t, f.hooks.failed)
	f.hooks.completedErr = nil
	require.NoError(t, f.k.ProcessCompletedPostProcessRetries(f.ctx))
	queued, err = store.Has(queueKey)
	require.NoError(t, err)
	require.False(t, queued)
	require.Zero(t, f.hooks.failed)
}
