package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bls/types"
	blst "github.com/supranational/blst/bindings/go"
	"golang.org/x/crypto/sha3"
)

// ReissueThresholdSignature signs the immutable payload of a completed bridge
// request under a newer epoch. The bridge module must authorize the caller and
// select the current signing epoch before invoking this method. Initial bridge
// admission records ReissueSigners only for destinations with global request-ID
// deduplication; legacy requests therefore cannot enter this path.
func (k Keeper) ReissueThresholdSignature(ctx sdk.Context, requestID []byte, newEpoch uint64) error {
	if len(requestID) != 32 {
		return fmt.Errorf("reissue request id must be 32 bytes")
	}
	request, err := k.GetSigningStatus(ctx, requestID)
	if err != nil {
		return err
	}
	if len(request.ReissueSigners) == 0 {
		return fmt.Errorf("request %x was not admitted for signature reissue", requestID)
	}
	if request.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED {
		return fmt.Errorf("only completed requests can be reissued, current status: %s", request.Status.String())
	}
	if !validCompletedSignature(request.FinalSignature) {
		return fmt.Errorf("request %x has no valid completed signature to preserve", requestID)
	}
	if newEpoch <= request.CurrentEpochId || newEpoch-request.CurrentEpochId < 2 {
		return fmt.Errorf("signature reissue requires an epoch at least two newer than the last completed signature")
	}
	if newEpoch <= request.LastReissueEpochId {
		return fmt.Errorf("request %x has already attempted reissue in epoch %d", requestID, request.LastReissueEpochId)
	}
	if request.ReissueCount == ^uint64(0) {
		return fmt.Errorf("request %x exhausted its reissue counter", requestID)
	}
	epochData, err := k.GetEpochBLSData(ctx, newEpoch)
	if err != nil {
		return fmt.Errorf("failed to get reissue epoch %d BLS data: %w", newEpoch, err)
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get reissue parameters: %w", err)
	}
	if err := k.validateThresholdSigningEpochPhase(ctx, &epochData, params); err != nil {
		return err
	}
	if len(epochData.GroupPublicKey) == 0 {
		return fmt.Errorf("reissue epoch %d has no group public key", newEpoch)
	}
	deadlineBlocks := params.SigningDeadlineBlocks
	if deadlineBlocks <= 0 {
		deadlineBlocks = types.DefaultParams().SigningDeadlineBlocks
	}
	deadline := ctx.BlockHeight() + deadlineBlocks
	if deadline <= ctx.BlockHeight() {
		return fmt.Errorf("invalid reissue signing deadline")
	}

	// All cleanup, the new request, its deadline and the event commit together.
	cacheCtx, commit := ctx.CacheContext()
	if err := k.DeleteThresholdPartialSignaturesForRequest(cacheCtx, requestID); err != nil {
		return fmt.Errorf("failed to clear partial signatures before reissue: %w", err)
	}
	store := k.storeService.OpenKVStore(cacheCtx)
	if err := store.Delete(types.ExpirationIndexKey(request.DeadlineBlockHeight, requestID)); err != nil {
		return fmt.Errorf("failed to remove prior signing deadline: %w", err)
	}
	if err := store.Delete(types.CompletedPostProcessRetryKey(requestID)); err != nil {
		return fmt.Errorf("failed to remove prior completion retry: %w", err)
	}
	request.LastCompletedEpochId = request.CurrentEpochId
	request.LastCompletedSignature = append([]byte(nil), request.FinalSignature...)
	request.LastReissueEpochId = newEpoch
	request.ReissueCount++
	request.CurrentEpochId = newEpoch
	request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES
	request.FinalSignature = nil
	request.PartialSignatures = nil
	request.DeadlineBlockHeight = deadline
	request.Attempt = 1
	k.setSigningRequestPayloadHash(request)
	if err := k.storeThresholdSigningRequest(cacheCtx, request); err != nil {
		return err
	}
	if err := store.Set(types.ExpirationIndexKey(deadline, requestID), []byte{}); err != nil {
		return fmt.Errorf("failed to index reissue deadline: %w", err)
	}
	if err := cacheCtx.EventManager().EmitTypedEvent(&types.EventThresholdSigningRequested{
		RequestId: requestID, CurrentEpochId: newEpoch,
		EncodedData: request.EncodedData, MessageHash: request.MessageHash,
		DeadlineBlockHeight: deadline,
	}); err != nil {
		return fmt.Errorf("failed to emit signature reissue request: %w", err)
	}
	commit()
	return nil
}

func validCompletedSignature(signature []byte) bool {
	if len(signature) != 48 {
		return false
	}
	point := new(blst.P1Affine).Uncompress(signature)
	return point != nil && point.SigValidate(true)
}

func hasReissueBackup(request *types.ThresholdSigningRequest) bool {
	return len(request.LastCompletedSignature) != 0 && request.CurrentEpochId != request.LastCompletedEpochId
}

func (k Keeper) setSigningRequestPayloadHash(request *types.ThresholdSigningRequest) {
	request.EncodedData = k.encodeSigningData(types.SigningData{
		CurrentEpochId: request.CurrentEpochId, ChainId: request.ChainId,
		RequestId: request.RequestId, Data: request.Data,
	})
	hash := sha3.NewLegacyKeccak256()
	hash.Write(request.EncodedData)
	request.MessageHash = hash.Sum(nil)
}

// A signature that once completed must never flow through failure/refund hooks.
// Preserve its epoch and proof on reissue failure, while keeping the attempted
// epoch so another transaction cannot restart the same reissue indefinitely.
func (k Keeper) restoreCompletedAfterReissueFailure(ctx sdk.Context, request *types.ThresholdSigningRequest, reason string) error {
	if !validCompletedSignature(request.LastCompletedSignature) {
		return fmt.Errorf("cannot restore request %x: invalid completed signature backup", request.RequestId)
	}
	cacheCtx, commit := ctx.CacheContext()
	failedEpoch := request.CurrentEpochId
	store := k.storeService.OpenKVStore(cacheCtx)
	if err := store.Delete(types.ExpirationIndexKey(request.DeadlineBlockHeight, request.RequestId)); err != nil {
		return fmt.Errorf("failed to remove reissue deadline: %w", err)
	}
	if err := k.DeleteThresholdPartialSignaturesForRequest(cacheCtx, request.RequestId); err != nil {
		return fmt.Errorf("failed to clear failed reissue partial signatures: %w", err)
	}
	request.CurrentEpochId = request.LastCompletedEpochId
	request.FinalSignature = append([]byte(nil), request.LastCompletedSignature...)
	request.PartialSignatures = nil
	request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
	request.DeadlineBlockHeight = 0
	k.setSigningRequestPayloadHash(request)
	if err := k.storeThresholdSigningRequest(cacheCtx, request); err != nil {
		return err
	}
	// Reissue removed any outstanding cleanup retry for the earlier completed
	// signature. Its cleanup is idempotent, so retry it on restoration and keep
	// a durable queue entry if it still fails. Never use the failure/refund hook.
	if err := k.runThresholdSigningCompletedPostProcess(cacheCtx, request.RequestId, request.CurrentEpochId); err != nil {
		if queueErr := store.Set(types.CompletedPostProcessRetryKey(request.RequestId), []byte{}); queueErr != nil {
			return fmt.Errorf("failed to queue restored completion cleanup: %w", queueErr)
		}
	}
	cacheCtx.EventManager().EmitEvent(sdk.NewEvent("threshold_signing_reissue_restored",
		sdk.NewAttribute("request_id", fmt.Sprintf("%x", request.RequestId)),
		sdk.NewAttribute("failed_epoch_id", fmt.Sprintf("%d", failedEpoch)),
		sdk.NewAttribute("completed_epoch_id", fmt.Sprintf("%d", request.CurrentEpochId)),
		sdk.NewAttribute("reason", reason),
	))
	commit()
	return nil
}
