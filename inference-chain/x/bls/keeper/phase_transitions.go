package keeper
import (
"fmt"
"sort"
sdk "github.com/cosmos/cosmos-sdk/types"
bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
"github.com/productscience/inference/x/bls/types"
)
func (k Keeper) ProcessDKGPhaseTransitions(ctx sdk.Context) error {
activeEpochID, found := k.GetActiveEpochID(ctx)
if !found || activeEpochID == 0 {
return nil
}
return k.ProcessDKGPhaseTransitionForEpoch(ctx, activeEpochID)
}
func (k Keeper) ProcessDKGPhaseTransitionForEpoch(ctx sdk.Context, epochID uint64) error {
epochBLSData, found := k.GetEpochBLSData(ctx, epochID)
if !found {
return fmt.Errorf("EpochBLSData not found for epoch %d", epochID)
}
switch epochBLSData.DkgPhase {
case types.DKGPhase_DKG_PHASE_COMPLETED,
types.DKGPhase_DKG_PHASE_SIGNED,
types.DKGPhase_DKG_PHASE_FAILED:
return nil
}
if err := k.validateEpochStructure(&epochBLSData); err != nil {
return k.failDKG(ctx, &epochBLSData, fmt.Sprintf("invalid epoch structure: %v", err))
}
currentBlockHeight := ctx.BlockHeight()
switch epochBLSData.DkgPhase {
case types.DKGPhase_DKG_PHASE_DEALING:
if currentBlockHeight >= epochBLSData.DealingPhaseDeadlineBlock {
if err := k.TransitionToVerifyingPhase(ctx, &epochBLSData); err != nil {
return fmt.Errorf("failed to transition DKG to verifying phase for epoch %d: %w", epochID, err)
}
}
case types.DKGPhase_DKG_PHASE_VERIFYING:
if currentBlockHeight >= epochBLSData.VerifyingPhaseDeadlineBlock {
if err := k.CompleteDKG(ctx, &epochBLSData); err != nil {
return fmt.Errorf("failed to complete DKG for epoch %d: %w", epochID, err)
}
}
}
return nil
}
func (k Keeper) TransitionToVerifyingPhase(ctx sdk.Context, epochBLSData *types.EpochBLSData) error {
if epochBLSData == nil {
return fmt.Errorf("epochBLSData is nil")
}
if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_DEALING {
return fmt.Errorf(
"DKG for epoch %d is not in DEALING phase, current phase: %s",
epochBLSData.EpochId,
epochBLSData.DkgPhase.String(),
)
}
slotsWithDealerParts, err := k.CalculateSlotsWithDealerParts(epochBLSData)
if err != nil {
return k.failDKG(ctx, epochBLSData, fmt.Sprintf("invalid dealing participation data: %v", err))
}
requiredSlots := majoritySlots(epochBLSData.ITotalSlots)
k.Logger().Info("Checking DKG participation",
"epochId", epochBLSData.EpochId,
"slotsWithDealerParts", slotsWithDealerParts,
"totalSlots", epochBLSData.ITotalSlots,
"requiredSlots", requiredSlots)
if slotsWithDealerParts < requiredSlots {
return k.failDKG(
ctx,
epochBLSData,
fmt.Sprintf(
"insufficient participation in dealing phase: %d slots with dealer parts out of %d total slots (required: >= %d)",
slotsWithDealerParts,
epochBLSData.ITotalSlots,
requiredSlots,
),
)
}
params := k.GetParams(ctx)
currentBlockHeight := ctx.BlockHeight()
updated := *epochBLSData
updated.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING
updated.VerifyingPhaseDeadlineBlock = currentBlockHeight + params.VerificationPhaseDurationBlocks
if err := ctx.EventManager().EmitTypedEvent(&types.EventVerifyingPhaseStarted{
EpochId: updated.EpochId,
VerifyingPhaseDeadlineBlock: uint64(updated.VerifyingPhaseDeadlineBlock),
EpochData: updated,
}); err != nil {
return fmt.Errorf("failed to emit EventVerifyingPhaseStarted for epoch %d: %w", updated.EpochId, err)
}
k.SetEpochBLSData(ctx, updated)
*epochBLSData = updated
k.Logger().Info("DKG transitioned to VERIFYING phase",
"epochId", updated.EpochId,
"verifyingDeadline", updated.VerifyingPhaseDeadlineBlock)
return nil
}
func (k Keeper) CompleteDKG(ctx sdk.Context, epochBLSData *types.EpochBLSData) error {
if epochBLSData == nil {
return fmt.Errorf("epochBLSData is nil")
}
if epochBLSData.DkgPhase != types.DKGPhase_DKG_PHASE_VERIFYING {
return fmt.Errorf(
"DKG for epoch %d is not in VERIFYING phase, current phase: %s",
epochBLSData.EpochId,
epochBLSData.DkgPhase.String(),
)
}
slotsWithVerification, err := k.CalculateSlotsWithVerificationVectors(epochBLSData)
if err != nil {
return k.failDKG(ctx, epochBLSData, fmt.Sprintf("invalid verification participation data: %v", err))
}
requiredSlots := majoritySlots(epochBLSData.ITotalSlots)
k.Logger().Info("Checking DKG verification participation",
"epochId", epochBLSData.EpochId,
"slotsWithVerification", slotsWithVerification,
"totalSlots", epochBLSData.ITotalSlots,
"requiredSlots", requiredSlots)
if slotsWithVerification < requiredSlots {
return k.failDKG(
ctx,
epochBLSData,
fmt.Sprintf(
"insufficient participation in verification phase: %d slots with verification vectors out of %d total slots (required: >= %d)",
slotsWithVerification,
epochBLSData.ITotalSlots,
requiredSlots,
),
)
}
validDealers, err := k.DetermineValidDealersWithConsensus(epochBLSData)
if err != nil {
return k.failDKG(ctx, epochBLSData, fmt.Sprintf("failed to determine valid dealers: %v", err))
}
groupPublicKey, aggregatedDealerCount, err := k.ComputeGroupPublicKey(epochBLSData, validDealers)
if err != nil {
return k.failDKG(ctx, epochBLSData, fmt.Sprintf("failed to compute group public key: %v", err))
}
minDealersRequired := minimumDealersRequired(epochBLSData)
if aggregatedDealerCount < minDealersRequired {
return k.failDKG(
ctx,
epochBLSData,
fmt.Sprintf(
"insufficient valid dealers for final aggregation: got %d, required at least %d",
aggregatedDealerCount,
minDealersRequired,
),
)
}
updated := *epochBLSData
updated.GroupPublicKey = groupPublicKey
updated.DkgPhase = types.DKGPhase_DKG_PHASE_COMPLETED
updated.ValidDealers = validDealers
if err := ctx.EventManager().EmitTypedEvent(&types.EventGroupPublicKeyGenerated{
EpochId: updated.EpochId,
GroupPublicKey: groupPublicKey,
ITotalSlots: updated.ITotalSlots,
TSlotsDegree: updated.TSlotsDegree,
EpochData: updated,
ChainId: ctx.ChainID(),
}); err != nil {
return fmt.Errorf("failed to emit EventGroupPublicKeyGenerated for epoch %d: %w", updated.EpochId, err)
}
k.SetEpochBLSData(ctx, updated)
k.ClearActiveEpochID(ctx)
*epochBLSData = updated
k.Logger().Info("DKG completed successfully",
"epochId", updated.EpochId,
"validDealersCount", aggregatedDealerCount,
"groupPublicKeySize", len(groupPublicKey))
return nil
}
func (k Keeper) CalculateSlotsWithDealerParts(epochBLSData *types.EpochBLSData) (uint32, error) {
activeParticipants := make([]bool, len(epochBLSData.Participants))
for i, dealerPart := range epochBLSData.DealerParts {
if i >= len(activeParticipants) {
break
}
if dealerPart != nil && dealerPart.DealerAddress != "" && len(dealerPart.Commitments) > 0 {
activeParticipants[i] = true
}
}
return k.calculateUniqueCoveredSlots(epochBLSData, activeParticipants)
}
func (k Keeper) CalculateSlotsWithVerificationVectors(epochBLSData *types.EpochBLSData) (uint32, error) {
activeParticipants := make([]bool, len(epochBLSData.Participants))
for i := range epochBLSData.Participants {
if i >= len(epochBLSData.VerificationSubmissions) {
continue
}
verification := epochBLSData.VerificationSubmissions[i]
if verification != nil && len(verification.DealerValidity) > 0 {
activeParticipants[i] = true
}
}
return k.calculateUniqueCoveredSlots(epochBLSData, activeParticipants)
}
// DetermineValidDealersWithConsensus uses SLOT-WEIGHTED voting.
// A verifier's vote weight equals the number of slots owned by that verifier.
func (k Keeper) DetermineValidDealersWithConsensus(epochBLSData *types.EpochBLSData) ([]bool, error) {
if epochBLSData == nil {
return nil, fmt.Errorf("epochBLSData is nil")
}
participantCount := len(epochBLSData.Participants)
if participantCount == 0 {
return nil, fmt.Errorf("no participants found for epoch %d", epochBLSData.EpochId)
}
validDealers := make([]bool, participantCount)
requiredSlotVotes := majoritySlots(epochBLSData.ITotalSlots)
for dealerIndex := 0; dealerIndex < participantCount; dealerIndex++ {
var validSlotVotes uint32
var totalSlotVotes uint32
for verifierIndex, verification := range epochBLSData.VerificationSubmissions {
if verification == nil || len(verification.DealerValidity) == 0 {
continue
}
if verifierIndex >= len(epochBLSData.Participants) {
continue
}
if dealerIndex >= len(verification.DealerValidity) {
continue
}
participant := epochBLSData.Participants[verifierIndex]
if participant.SlotEndIndex < participant.SlotStartIndex {	return nil, fmt.Errorf( 		"invalid slot range for verifier %d in epoch %d: start=%d end=%d", 		verifierIndex, 		epochBLSData.EpochId, 		participant.SlotStartIndex, 		participant.SlotEndIndex, 	) }
if participant.SlotStartIndex >= epochBLSData.ITotalSlots || participant.SlotEndIndex >= epochBLSData.ITotalSlots {
return nil, fmt.Errorf(
"slot range out of bounds for verifier %d in epoch %d: start=%d end=%d totalSlots=%d",
verifierIndex,
epochBLSData.EpochId,
participant.SlotStartIndex,
participant.SlotEndIndex,
epochBLSData.ITotalSlots,
)
}
slotWeight := participant.SlotEndIndex - participant.SlotStartIndex + 1
if slotWeight > epochBLSData.ITotalSlots {
return nil, fmt.Errorf(
"slot weight too large for verifier %d in epoch %d: weight=%d totalSlots=%d",
verifierIndex,
epochBLSData.EpochId,
slotWeight,
epochBLSData.ITotalSlots,
)
}
if totalSlotVotes > epochBLSData.ITotalSlots-slotWeight {
return nil, fmt.Errorf(
"slot vote accumulation overflow for dealer %d in epoch %d: current=%d add=%d total=%d",
dealerIndex,
epochBLSData.EpochId,
totalSlotVotes,
slotWeight,
epochBLSData.ITotalSlots,
)
}
totalSlotVotes += slotWeight
if verification.DealerValidity[dealerIndex] { 	if validSlotVotes > epochBLSData.ITotalSlots-slotWeight { 		return nil, fmt.Errorf( 			"valid slot vote accumulation overflow for dealer %d in epoch %d: current=%d add=%d total=%d", 			dealerIndex, 			epochBLSData.EpochId, 			validSlotVotes, 			slotWeight, 			epochBLSData.ITotalSlots, 		) 	} 	validSlotVotes += slotWeight }
}
dealerSubmittedParts := dealerIndex < len(epochBLSData.DealerParts) &&
epochBLSData.DealerParts[dealerIndex] != nil &&
epochBLSData.DealerParts[dealerIndex].DealerAddress != "" &&
len(epochBLSData.DealerParts[dealerIndex].Commitments) > 0
dealerIsValid := totalSlotVotes >= requiredSlotVotes && validSlotVotes >= majoritySlots(totalSlotVotes)
validDealers[dealerIndex] = dealerIsValid && dealerSubmittedParts
}
return validDealers, nil
}
func (k Keeper) ComputeGroupPublicKey(epochBLSData *types.EpochBLSData, validDealers []bool) ([]byte, int, error) {
if epochBLSData == nil {
return nil, 0, fmt.Errorf("epochBLSData is nil")
}
if len(validDealers) != len(epochBLSData.Participants) {
return nil, 0, fmt.Errorf(
"validDealers length mismatch for epoch %d: got %d, expected %d",
epochBLSData.EpochId,
len(validDealers),
len(epochBLSData.Participants),
)
}
var (
groupPublicKey bls12381.G2Affine
accInitialized bool
aggregated int
)
k.Logger().Info("Starting group public key computation", "epochId", epochBLSData.EpochId)
for dealerIndex, dealerIsValid := range validDealers {
if !dealerIsValid {
continue
}
if dealerIndex >= len(epochBLSData.DealerParts) {
k.Logger().Warn("Invalid dealer index", "dealerIndex", dealerIndex, "totalDealers", len(epochBLSData.DealerParts))
continue
}
dealerPart := epochBLSData.DealerParts[dealerIndex]
if dealerPart == nil {
k.Logger().Warn("Nil dealer part", "dealerIndex", dealerIndex)
continue
}
if dealerPart.DealerAddress == "" {
k.Logger().Warn("Empty dealer address", "dealerIndex", dealerIndex)
continue
}
if len(dealerPart.Commitments) == 0 {
k.Logger().Warn("No commitments found for dealer", "dealerIndex", dealerIndex)
continue
}
commitmentBytes := dealerPart.Commitments[0]
if len(commitmentBytes) != 96 {
return nil, 0, fmt.Errorf(
"invalid commitment size for dealer %d: expected 96 bytes, got %d",
dealerIndex,
len(commitmentBytes),
)
}
var commitment bls12381.G2Affine
if err := commitment.Unmarshal(commitmentBytes); err != nil {
return nil, 0, fmt.Errorf("failed to unmarshal G2 commitment for dealer %d: %w", dealerIndex, err)
}
if !commitment.IsOnCurve() {
return nil, 0, fmt.Errorf("commitment for dealer %d is not on curve", dealerIndex)
}
if !accInitialized {
groupPublicKey = commitment
accInitialized = true
} else {
groupPublicKey.Add(&groupPublicKey, &commitment)
}
aggregated++
k.Logger().Debug("Added dealer commitment to group public key",
"dealerIndex", dealerIndex,
"dealerAddress", dealerPart.DealerAddress)
}
if !accInitialized || aggregated == 0 {
return nil, 0, fmt.Errorf("no valid commitments aggregated for epoch %d", epochBLSData.EpochId)
}
groupPublicKeyBytes := groupPublicKey.Bytes()
k.Logger().Info("Completed group public key computation",
"epochId", epochBLSData.EpochId,
"aggregatedDealers", aggregated,
"groupPublicKeySize", len(groupPublicKeyBytes))
return groupPublicKeyBytes[:], aggregated, nil
}
func (k Keeper) failDKG(ctx sdk.Context, epochBLSData *types.EpochBLSData, reason string) error {
if epochBLSData == nil {
return fmt.Errorf("cannot fail DKG: epochBLSData is nil")
}
updated := *epochBLSData
updated.DkgPhase = types.DKGPhase_DKG_PHASE_FAILED
if err := ctx.EventManager().EmitTypedEvent(&types.EventDKGFailed{
EpochId: updated.EpochId,
Reason: reason,
EpochData: updated,
}); err != nil {
return fmt.Errorf("failed to emit EventDKGFailed for epoch %d: %w", updated.EpochId, err)
}
k.SetEpochBLSData(ctx, updated)
k.ClearActiveEpochID(ctx)
*epochBLSData = updated
k.Logger().Info("DKG marked as FAILED",
"epochId", updated.EpochId,
"reason", reason)
return nil
}
func (k Keeper) calculateUniqueCoveredSlots(epochBLSData *types.EpochBLSData, activeParticipants []bool) (uint32, error) {
if epochBLSData == nil {
return 0, fmt.Errorf("epochBLSData is nil")
}
if len(activeParticipants) != len(epochBLSData.Participants) {
return 0, fmt.Errorf(
"activeParticipants length mismatch for epoch %d: got %d, expected %d",
epochBLSData.EpochId,
len(activeParticipants),
len(epochBLSData.Participants),
)
}
type slotRange struct {
index int
start uint32
end uint32
}
ranges := make([]slotRange, 0, len(epochBLSData.Participants))
for i, participant := range epochBLSData.Participants {
if !activeParticipants[i] {
continue
}
start := participant.SlotStartIndex
end := participant.SlotEndIndex
if end < start {
return 0, fmt.Errorf(
"invalid slot range for participant %d in epoch %d: start=%d end=%d",
i, epochBLSData.EpochId, start, end,
)
}
if start >= epochBLSData.ITotalSlots || end >= epochBLSData.ITotalSlots {
return 0, fmt.Errorf(
"slot range out of bounds for participant %d in epoch %d: start=%d end=%d totalSlots=%d",
i, epochBLSData.EpochId, start, end, epochBLSData.ITotalSlots,
)
}
ranges = append(ranges, slotRange{
index: i,
start: start,
end: end,
})
}
if len(ranges) == 0 {
return 0, nil
}
sort.Slice(ranges, func(i, j int) bool {
if ranges[i].start == ranges[j].start {
return ranges[i].end < ranges[j].end
}
return ranges[i].start < ranges[j].start
})
var totalSlots uint32
prev := ranges[0]
totalSlots = prev.end - prev.start + 1
for i := 1; i < len(ranges); i++ {
curr := ranges[i]
if curr.start <= prev.end {
return 0, fmt.Errorf(
"overlapping slot ranges in epoch %d: participant %d [%d,%d] overlaps participant %d [%d,%d]",
epochBLSData.EpochId,
prev.index, prev.start, prev.end,
curr.index, curr.start, curr.end,
)
}
rangeSize := curr.end - curr.start + 1
if totalSlots > epochBLSData.ITotalSlots-rangeSize {
return 0, fmt.Errorf(
"slot accumulation overflow/overflow-like condition in epoch %d: current=%d add=%d total=%d",
epochBLSData.EpochId,
totalSlots,
rangeSize,
epochBLSData.ITotalSlots,
)
}
totalSlots += rangeSize
prev = curr
}
if totalSlots > epochBLSData.ITotalSlots {
return 0, fmt.Errorf(
"computed total slots exceed epoch total in epoch %d: computed=%d total=%d",
epochBLSData.EpochId,
totalSlots,
epochBLSData.ITotalSlots,
)
}
return totalSlots, nil
}
func (k Keeper) validateEpochStructure(epochBLSData *types.EpochBLSData) error {
if epochBLSData == nil {
return fmt.Errorf("epochBLSData is nil")
}
if epochBLSData.ITotalSlots == 0 {
return fmt.Errorf("ITotalSlots must be > 0")
}
if len(epochBLSData.Participants) == 0 {
return fmt.Errorf("participants must not be empty")
}
if epochBLSData.TSlotsDegree == 0 {
return fmt.Errorf("TSlotsDegree must be > 0")
}
if epochBLSData.TSlotsDegree >= epochBLSData.ITotalSlots {
return fmt.Errorf("TSlotsDegree must be less than ITotalSlots")
}
allActive := make([]bool, len(epochBLSData.Participants))
for i := range allActive {
allActive[i] = true
}
if _, err := k.calculateUniqueCoveredSlots(epochBLSData, allActive); err != nil {
return fmt.Errorf("invalid participant slot layout: %w", err)
}
return nil
}
func majoritySlots(total uint32) uint32 {
return total/2 + 1
}
func minimumDealersRequired(epochBLSData *types.EpochBLSData) int {
return int(epochBLSData.TSlotsDegree) + 1
}

