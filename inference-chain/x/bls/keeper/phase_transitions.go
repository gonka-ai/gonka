package keeper
import (
"encoding/binary"
"fmt"
"cosmossdk.io/store/prefix"
"github.com/cosmos/cosmos-sdk/runtime"
sdk "github.com/cosmos/cosmos-sdk/types"
"golang.org/x/crypto/sha3"
"github.com/productscience/inference/x/bls/types"
)
// RequestThresholdSignature is the main entry point for other modules to request BLS threshold signatures
func (k Keeper) RequestThresholdSignature(ctx sdk.Context, signingData types.SigningData) error {
// Validate current epoch has completed DKG
epochBLSData, found := k.GetEpochBLSData(ctx, signingData.CurrentEpochId)
if !found {
return fmt.Errorf("epoch BLS data not found for epoch %d", signingData.CurrentEpochId)
}
// Verify epoch has completed DKG (has group public key)
if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_COMPLETED &&
epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_SIGNED {
return fmt.Errorf(
"epoch %d DKG not completed, current phase: %s",
signingData.CurrentEpochId,
epochBLSData.DkgPhase.String(),
)
}
if len(epochBLSData.GroupPublicKey) == 0 {
return fmt.Errorf("epoch %d has no group public key", signingData.CurrentEpochId)
}
// Validate fixed-width inputs for encodePacked format
if len(signingData.ChainId) != 32 {
return fmt.Errorf("invalid chain_id length: expected 32 bytes, got %d", len(signingData.ChainId))
}
if len(signingData.RequestId) != 32 {
return fmt.Errorf("invalid request_id length: expected 32 bytes, got %d", len(signingData.RequestId))
}
if len(signingData.Data) == 0 {
return fmt.Errorf("signing data must not be empty")
}
if len(signingData.Data) > 128 {
return fmt.Errorf("too many data elements: %d", len(signingData.Data))
}
for i, dataElement := range signingData.Data {
if len(dataElement) != 32 {
return fmt.Errorf(
"invalid data element length at index %d: expected 32 bytes, got %d",
i,
len(dataElement),
)
}
}
// Validate uniqueness - ensure request_id doesn't already exist
key := types.ThresholdSigningRequestKey(signingData.RequestId)
kvStore := k.storeService.OpenKVStore(ctx)
existingValue, err := kvStore.Get(key)
if err != nil {
return fmt.Errorf("failed to check request uniqueness: %w", err)
}
if existingValue != nil {
return fmt.Errorf("request_id already exists: %x", signingData.RequestId)
}
activeRequests, err := k.ListActiveSigningRequests(ctx, signingData.CurrentEpochId)
if err != nil {
return fmt.Errorf("failed to list active signing requests: %w", err)
}
if len(activeRequests) >= 1000 {
return fmt.Errorf("too many active signing requests for epoch %d", signingData.CurrentEpochId)
}
// Encode data using Ethereum-compatible abi.encodePacked format
encodedData := k.encodeSigningData(signingData)
if len(encodedData) > 4096 {
return fmt.Errorf("encoded data too large: %d bytes", len(encodedData))
}
// Compute message hash using keccak256 (Ethereum-compatible)
hash := sha3.NewLegacyKeccak256()
hash.Write(encodedData)
messageHash := hash.Sum(nil)
if len(messageHash) != 32 {
return fmt.Errorf("invalid message hash size: expected 32 bytes, got %d", len(messageHash))
}
// Calculate deadline block height
params := k.GetParams(ctx)
deadlineBlockHeight := ctx.BlockHeight() + int64(params.SigningDeadlineBlocks)
// Create threshold signing request
request := &types.ThresholdSigningRequest{
RequestId: signingData.RequestId,
CurrentEpochId: signingData.CurrentEpochId,
ChainId: signingData.ChainId,
Data: signingData.Data,
EncodedData: encodedData,
MessageHash: messageHash,
Status: types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
PartialSignatures: []types.PartialSignature{},
FinalSignature: []byte{},
CreatedBlockHeight: ctx.BlockHeight(),
DeadlineBlockHeight: deadlineBlockHeight,
}
// Emit event first
err = ctx.EventManager().EmitTypedEvent(&types.EventThresholdSigningRequested{
RequestId: signingData.RequestId,
CurrentEpochId: signingData.CurrentEpochId,
EncodedData: encodedData,
MessageHash: messageHash,
DeadlineBlockHeight: deadlineBlockHeight,
})
if err != nil {
return fmt.Errorf("failed to emit threshold signing requested event: %w", err)
}
// Store the request
requestBytes := k.cdc.MustMarshal(request)
err = kvStore.Set(key, requestBytes)
if err != nil {
return fmt.Errorf("failed to store threshold signing request: %w", err)
}
// Store expiration index entry for efficient deadline management
expirationKey := types.ExpirationIndexKey(deadlineBlockHeight, signingData.RequestId)
err = kvStore.Set(expirationKey, []byte{})
if err != nil {
return fmt.Errorf("failed to store expiration index entry: %w", err)
}
return nil
}
// GetSigningStatus returns the status of a threshold signing request by request_id
func (k Keeper) GetSigningStatus(ctx sdk.Context, requestID []byte) (*types.ThresholdSigningRequest, error) {
if len(requestID) == 0 {
return nil, fmt.Errorf("empty requestID")
}
key := types.ThresholdSigningRequestKey(requestID)
kvStore := k.storeService.OpenKVStore(ctx)
requestBytes, err := kvStore.Get(key)
if err != nil {
return nil, fmt.Errorf("failed to get threshold signing request: %w", err)
}
if requestBytes == nil {
return nil, fmt.Errorf("threshold signing request not found: %x", requestID)
}
var request types.ThresholdSigningRequest
k.cdc.MustUnmarshal(requestBytes, &request)
return &request, nil
}
// ListActiveSigningRequests returns all active threshold signing requests for a given epoch
func (k Keeper) ListActiveSigningRequests(ctx sdk.Context, currentEpochID uint64) ([]*types.ThresholdSigningRequest, error) {
store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
signingStore := prefix.NewStore(store, types.ThresholdSigningRequestPrefix)
var activeRequests []*types.ThresholdSigningRequest
iterator := signingStore.Iterator(nil, nil)
defer iterator.Close()
for ; iterator.Valid(); iterator.Next() {
var request types.ThresholdSigningRequest
k.cdc.MustUnmarshal(iterator.Value(), &request)
if request.CurrentEpochId == currentEpochID && 	(request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_PENDING_SIGNING || 		request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES) { 	activeRequests = append(activeRequests, &request) } 
}
return activeRequests, nil
}
// encodeSigningData encodes signing data using Ethereum-compatible abi.encodePacked format
func (k Keeper) encodeSigningData(signingData types.SigningData) []byte {
var encoded []byte
epochBytes := make([]byte, 8)
binary.BigEndian.PutUint64(epochBytes, signingData.CurrentEpochId)
encoded = append(encoded, epochBytes...)
encoded = append(encoded, signingData.ChainId...)
encoded = append(encoded, signingData.RequestId...)
for _, dataElement := range signingData.Data {
encoded = append(encoded, dataElement...)
}
return encoded
}
// AddPartialSignature adds a partial signature to a threshold signing request and checks for completion
func (k Keeper) AddPartialSignature(ctx sdk.Context, requestID []byte, slotIndices []uint32, partialSignature []byte, submitter string) error {
if len(requestID) == 0 {
return fmt.Errorf("empty requestID")
}
request, err := k.GetSigningStatus(ctx, requestID)
if err != nil {
return err
}
if request.Status != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES {
return fmt.Errorf("request is not collecting signatures, current status: %s", request.Status.String())
}
if ctx.BlockHeight() > request.DeadlineBlockHeight {
request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED
k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId)
if err := k.storeThresholdSigningRequest(ctx, request); err != nil { 	return err } return k.emitThresholdSigningFailed(ctx, requestID, request.CurrentEpochId, "request expired") 
}
if len(slotIndices) == 0 {
return fmt.Errorf("no slot indices provided")
}
if len(slotIndices) > 1024 {
return fmt.Errorf("too many slot indices submitted: %d", len(slotIndices))
}
if len(partialSignature) != 96 {
return fmt.Errorf("invalid partial signature length: expected 96 bytes, got %d", len(partialSignature))
}
if submitter == "" {
return fmt.Errorf("empty submitter")
}
epochBLSData, found := k.GetEpochBLSData(ctx, request.CurrentEpochId)
if !found {
return fmt.Errorf("epoch BLS data not found for epoch %d", request.CurrentEpochId)
}
if err := k.validateSlotOwnership(ctx, submitter, slotIndices, &epochBLSData); err != nil {
return fmt.Errorf("slot ownership validation failed: %w", err)
}
if err := k.verifyPartialSignature(partialSignature, request.MessageHash, slotIndices, &epochBLSData); err != nil {
return fmt.Errorf("partial signature verification failed: %w", err)
}
for _, existingSig := range request.PartialSignatures {
if existingSig.ParticipantAddress == submitter {
return fmt.Errorf("participant %s already submitted partial signature", submitter)
}
}
if len(request.PartialSignatures) >= int(epochBLSData.ITotalSlots) {
return fmt.Errorf("too many partial signatures submitted")
}
request.PartialSignatures = append(request.PartialSignatures, types.PartialSignature{
ParticipantAddress: submitter,
SlotIndices: slotIndices,
Signature: partialSignature,
})
if err := k.checkThresholdAndAggregate(ctx, request, &epochBLSData); err != nil {
return fmt.Errorf("threshold check and aggregation failed: %w", err)
}
if request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED ||
request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED {
return nil
}
return k.storeThresholdSigningRequest(ctx, request)
}
// validateSlotOwnership checks if the submitter owns the claimed slot indices
func (k Keeper) validateSlotOwnership(ctx sdk.Context, submitter string, slotIndices []uint32, epochBLSData *types.EpochBLSData) error {
var participantStartSlot, participantEndSlot uint32
found := false
for _, participant := range epochBLSData.Participants {
if participant.Address == submitter {
participantStartSlot = participant.SlotStartIndex
participantEndSlot = participant.SlotEndIndex
found = true
break
}
}
if !found {
return fmt.Errorf("submitter %s not found in epoch %d participants", submitter, epochBLSData.EpochId)
}
seen := make(map[uint32]struct{})
for _, claimedSlot := range slotIndices {
if claimedSlot < participantStartSlot || claimedSlot > participantEndSlot {
return fmt.Errorf(
"submitter %s does not own slot %d (valid range: %d-%d)",
submitter,
claimedSlot,
participantStartSlot,
participantEndSlot,
)
}
if _, exists := seen[claimedSlot]; exists { 	return fmt.Errorf("duplicate slot index %d submitted by %s", claimedSlot, submitter) } seen[claimedSlot] = struct{}{} 
}
return nil
}
// verifyPartialSignature verifies a partial signature against the message hash
func (k Keeper) verifyPartialSignature(partialSignature []byte, messageHash []byte, slotIndices []uint32, epochBLSData *types.EpochBLSData) error {
if len(partialSignature) != 96 {
return fmt.Errorf("invalid partial signature length: expected 96 bytes, got %d", len(partialSignature))
}
if len(messageHash) != 32 {
return fmt.Errorf("invalid message hash length: expected 32 bytes, got %d", len(messageHash))
}
if len(slotIndices) == 0 {
return fmt.Errorf("no slot indices provided")
}
if !k.verifyBLSPartialSignature(partialSignature, messageHash, epochBLSData, slotIndices) {
return fmt.Errorf("BLS signature verification failed")
}
return nil
}
// checkThresholdAndAggregate checks if enough partial signatures collected and aggregates them
func (k Keeper) checkThresholdAndAggregate(ctx sdk.Context, request *types.ThresholdSigningRequest, epochBLSData *types.EpochBLSData) error {
if request == nil {
return fmt.Errorf("request is nil")
}
if epochBLSData == nil {
return fmt.Errorf("epochBLSData is nil")
}
uniqueSlots := make(map[uint32]struct{})
if len(request.PartialSignatures) > 10000 {
return fmt.Errorf("too many partial signatures")
}
for sigIndex, partialSig := range request.PartialSignatures {
for _, slotIdx := range partialSig.SlotIndices {
if slotIdx >= epochBLSData.ITotalSlots {
return fmt.Errorf(
"slot index out of bounds in partial signature %d: slot=%d totalSlots=%d",
sigIndex,
slotIdx,
epochBLSData.ITotalSlots,
)
}
	if _, exists := uniqueSlots[slotIdx]; exists { 		return fmt.Errorf( 			"duplicate slot index %d detected across partial signatures at partial signature %d", 			slotIdx, 			sigIndex, 		) 	} 	uniqueSlots[slotIdx] = struct{}{} } 
}
if uint32(len(uniqueSlots)) > epochBLSData.ITotalSlots {
return fmt.Errorf("covered slots exceed total slots")
}
totalSlotsCovered := uint32(len(uniqueSlots))
totalSlots := epochBLSData.ITotalSlots
threshold := totalSlots/2 + 1
if totalSlotsCovered < threshold {
return nil
}
finalSignature, err := k.aggregatePartialSignatures(request.PartialSignatures, epochBLSData)
if err != nil {
request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_FAILED
request.FinalSignature = []byte{}
k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId) if errStore := k.storeThresholdSigningRequest(ctx, request); errStore != nil { 	return fmt.Errorf("failed to store failed threshold signing request: %w", errStore) } return k.emitThresholdSigningFailed( 	ctx, 	request.RequestId, 	request.CurrentEpochId, 	fmt.Sprintf("signature aggregation failed: %v", err), ) 
}
request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
request.FinalSignature = finalSignature
k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, request.RequestId)
if errStore := k.storeThresholdSigningRequest(ctx, request); errStore != nil {
return fmt.Errorf("failed to store completed threshold signing request: %w", errStore)
}
return k.emitThresholdSigningCompleted(
ctx,
request.RequestId,
request.CurrentEpochId,
finalSignature,
totalSlotsCovered,
)
}
// aggregatePartialSignatures combines partial signatures into final signature
func (k Keeper) aggregatePartialSignatures(partialSigs []types.PartialSignature, _ *types.EpochBLSData) ([]byte, error) {
if len(partialSigs) == 0 {
return nil, fmt.Errorf("no partial signatures to aggregate")
}
return k.aggregateBLSPartialSignatures(partialSigs)
}
// storeThresholdSigningRequest stores a threshold signing request
func (k Keeper) storeThresholdSigningRequest(ctx sdk.Context, request *types.ThresholdSigningRequest) error {
key := types.ThresholdSigningRequestKey(request.RequestId)
kvStore := k.storeService.OpenKVStore(ctx)
requestBytes := k.cdc.MustMarshal(request)
return kvStore.Set(key, requestBytes)
}
// emitThresholdSigningCompleted emits completion event
func (k Keeper) emitThresholdSigningCompleted(ctx sdk.Context, requestID []byte, epochID uint64, finalSignature []byte, participatingSlots uint32) error {
return ctx.EventManager().EmitTypedEvent(&types.EventThresholdSigningCompleted{
RequestId: requestID,
CurrentEpochId: epochID,
FinalSignature: finalSignature,
ParticipatingSlots: participatingSlots,
})
}
// emitThresholdSigningFailed emits failure event
func (k Keeper) emitThresholdSigningFailed(ctx sdk.Context, requestID []byte, epochID uint64, reason string) error {
return ctx.EventManager().EmitTypedEvent(&types.EventThresholdSigningFailed{
RequestId: requestID,
CurrentEpochId: epochID,
Reason: reason,
})
}
// removeFromExpirationIndex removes a request from the expiration index
func (k Keeper) removeFromExpirationIndex(ctx sdk.Context, deadlineBlockHeight int64, requestID []byte) {
kvStore := k.storeService.OpenKVStore(ctx)
expirationKey := types.ExpirationIndexKey(deadlineBlockHeight, requestID)
_ = kvStore.Delete(expirationKey)
}
// ProcessThresholdSigningDeadlines processes expired threshold signing requests efficiently using expiration index
func (k Keeper) ProcessThresholdSigningDeadlines(ctx sdk.Context) error {
currentBlockHeight := ctx.BlockHeight()
store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
expirationPrefix := types.ExpirationIndexPrefixForBlock(currentBlockHeight)
expirationStore := prefix.NewStore(store, expirationPrefix)
iterator := expirationStore.Iterator(nil, nil)
defer iterator.Close()
var expiredCount uint32
for ; iterator.Valid(); iterator.Next() {
requestID := iterator.Key()
request, err := k.GetSigningStatus(ctx, requestID) if err != nil { 	k.Logger().Error( 		"Failed to load threshold signing request for deadline processing", 		"request_id", fmt.Sprintf("%x", requestID), 		"error", err, 	) 	continue } if request.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES && 	currentBlockHeight >= request.DeadlineBlockHeight { 	request.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED 	if err := k.storeThresholdSigningRequest(ctx, request); err != nil { 		k.Logger().Error( 			"Failed to store expired threshold signing request", 			"request_id", fmt.Sprintf("%x", requestID), 			"error", err, 		) 		continue 	} 	k.removeFromExpirationIndex(ctx, request.DeadlineBlockHeight, requestID) 	if err := k.emitThresholdSigningFailed(ctx, requestID, request.CurrentEpochId, "deadline expired"); err != nil { 		k.Logger().Error( 			"Failed to emit threshold signing failed event", 			"request_id", fmt.Sprintf("%x", requestID), 			"error", err, 		) 	} 	expiredCount++ } 
}
if expiredCount > 0 {
k.Logger().Info(
"Processed expired threshold signing requests",
"block_height", currentBlockHeight,
"expired_count", expiredCount,
)
}
return nil
}
