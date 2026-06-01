package keeper

import (
	"fmt"
	"sort"

	"cosmossdk.io/math"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// dealerPartsStore returns a prefix.Store scoped to all dealer parts for a
// single epoch. Keys within the returned store are the sub-keys produced by
// types.DealerPartSubKey (4-byte big-endian participant index).
func (k Keeper) dealerPartsStore(ctx sdk.Context, epochID uint64) prefix.Store {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return prefix.NewStore(store, types.DealerPartEpochPrefix(epochID))
}

// verificationSubmissionsStore returns a prefix.Store scoped to all
// verification vector submissions for a single epoch. Keys within the
// returned store are the sub-keys produced by
// types.VerificationSubmissionSubKey (4-byte big-endian participant index).
func (k Keeper) verificationSubmissionsStore(ctx sdk.Context, epochID uint64) prefix.Store {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return prefix.NewStore(store, types.VerificationSubmissionEpochPrefix(epochID))
}

// dealerComplaintsStore returns a prefix.Store scoped to all dealer
// complaints for a single epoch. Keys within the returned store are the
// 8-byte sub-keys produced by types.DealerComplaintSubKey (dealer index
// then complainer index, both big-endian uint32).
func (k Keeper) dealerComplaintsStore(ctx sdk.Context, epochID uint64) prefix.Store {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return prefix.NewStore(store, types.DealerComplaintEpochPrefix(epochID))
}

// InitiateKeyGenerationForEpoch initiates DKG for a given epoch with finalized participants
func (k Keeper) InitiateKeyGenerationForEpoch(ctx sdk.Context, epochID uint64, finalizedParticipants []types.ParticipantWithWeightAndKey) error {
	// Get module parameters
	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get parameters: %w", err)
	}
	iTotalSlots := params.ITotalSlots
	tSlotsDegree := iTotalSlots - params.TSlotsDegreeOffset // Calculate t from offset

	// Perform deterministic slot assignment based on percentage weights
	blsParticipants, err := k.AssignSlots(ctx, finalizedParticipants, iTotalSlots)
	if err != nil {
		return fmt.Errorf("failed to assign slots: %w", err)
	}

	// Validate shape constraints before initiating
	if len(blsParticipants) > types.MaxEncryptedSharesParticipantsCount {
		return fmt.Errorf("number of participants %d exceeds maximum %d", len(blsParticipants), types.MaxEncryptedSharesParticipantsCount)
	}
	if tSlotsDegree+1 > uint32(types.MaxDealerPartCommitmentsCount) {
		return fmt.Errorf("tSlotsDegree+1 (%d) exceeds maximum commitments count %d", tSlotsDegree+1, types.MaxDealerPartCommitmentsCount)
	}

	for _, p := range blsParticipants {
		_, err := expectedEncryptedSharesCount(p)
		if err != nil {
			return fmt.Errorf("failed to validate encrypted share bounds for %s: %w", p.Address, err)
		}
	}

	// Calculate phase deadlines
	currentHeight := ctx.BlockHeight()
	dealingPhaseDeadline := currentHeight + params.DealingPhaseDurationBlocks
	verifyingPhaseDeadline := dealingPhaseDeadline + params.VerificationPhaseDurationBlocks

	// Initialize DealerParts array with empty objects (not nil pointers) to prevent marshaling panic
	dealerParts := make([]*types.DealerPartStorage, len(blsParticipants))
	for i := range dealerParts {
		dealerParts[i] = &types.DealerPartStorage{
			DealerAddress:     "", // Will be set when participant submits their part
			Commitments:       [][]byte{},
			ParticipantShares: []*types.EncryptedSharesForParticipant{},
		}
	}

	// Initialize VerificationSubmissions array with empty objects to use index-based access
	verificationSubmissions := make([]*types.VerificationVectorSubmission, len(blsParticipants))
	for i := range verificationSubmissions {
		verificationSubmissions[i] = &types.VerificationVectorSubmission{
			DealerValidity: []bool{}, // Empty array indicates no submission yet
		}
	}

	// Create EpochBLSData
	epochBLSData := types.EpochBLSData{
		EpochId:                     epochID,
		ITotalSlots:                 iTotalSlots,
		TSlotsDegree:                tSlotsDegree,
		Participants:                blsParticipants,
		DkgPhase:                    types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock:   dealingPhaseDeadline,
		VerifyingPhaseDeadlineBlock: verifyingPhaseDeadline,
		GroupPublicKey:              []byte{},
		DealerParts:                 dealerParts,
		VerificationSubmissions:     verificationSubmissions,
	}

	// Store the EpochBLSData
	if err := k.SetEpochBLSData(ctx, epochBLSData); err != nil {
		return fmt.Errorf("failed to store epoch %d BLS data: %w", epochID, err)
	}

	// Set this as the active epoch since only one DKG can be active at a time
	k.SetActiveEpochID(ctx, epochID)

	// Emit EventKeyGenerationInitiated
	event := types.EventKeyGenerationInitiated{
		EpochId:      epochID,
		ITotalSlots:  iTotalSlots,
		TSlotsDegree: tSlotsDegree,
		Participants: blsParticipants,
	}

	if err := ctx.EventManager().EmitTypedEvent(&event); err != nil {
		return fmt.Errorf("failed to emit key generation initiated event for epoch %d: %w", epochID, err)
	}

	k.Logger().Info(
		"DKG initiated for epoch",
		"epoch_id", epochID,
		"participants", len(blsParticipants),
		"total_slots", iTotalSlots,
		"t_degree", tSlotsDegree,
		"dealing_deadline", dealingPhaseDeadline,
	)

	return nil
}

// AssignSlots performs deterministic slot assignment based on percentage weights
func (k Keeper) AssignSlots(ctx sdk.Context, participants []types.ParticipantWithWeightAndKey, totalSlots uint32) ([]types.BLSParticipantInfo, error) {
	if len(participants) == 0 {
		return nil, fmt.Errorf("no participants provided")
	}

	// 1. Calculate total weight to normalize percentage values into ratios.
	totalWeight := math.LegacyZeroDec()
	for _, p := range participants {
		totalWeight = totalWeight.Add(p.PercentageWeight)
	}

	if totalWeight.IsZero() {
		return nil, fmt.Errorf("total weight is zero")
	}

	// 2. Sort by address so every node processes participants in exactly the same order.
	sortedParticipants := make([]types.ParticipantWithWeightAndKey, len(participants))
	copy(sortedParticipants, participants)
	sort.Slice(sortedParticipants, func(i, j int) bool {
		return sortedParticipants[i].Address < sortedParticipants[j].Address
	})

	// 3. Allocate floor(ratio * totalSlots) slots to each participant and remember the fractional remainders.
	// Slot allocation is strictly weight-based over the full participant set. Participants may receive zero slots.
	assigned := make([]int64, len(sortedParticipants))
	remainders := make([]math.LegacyDec, len(sortedParticipants))
	assignedTotal := int64(0)

	for i, participant := range sortedParticipants {
		if participant.PercentageWeight.IsZero() {
			continue
		}

		ratio := participant.PercentageWeight.Quo(totalWeight)
		slotDec := ratio.MulInt64(int64(totalSlots))
		floor := slotDec.TruncateInt64()
		remainder := slotDec.Sub(math.LegacyNewDec(floor))
		if remainder.IsNegative() {
			remainder = math.LegacyZeroDec()
		}

		assigned[i] = floor
		remainders[i] = remainder
		assignedTotal += floor
	}

	// Remaining slots are distributed by largest remainder, breaking ties by address.
	remaining := int64(totalSlots) - assignedTotal
	if remaining < 0 {
		return nil, fmt.Errorf("slot assignment error: floor allocations exceed total slots")
	}

	if remaining > 0 {
		indices := make([]int, 0, len(sortedParticipants))
		for i, p := range sortedParticipants {
			if p.PercentageWeight.IsZero() {
				continue
			}
			indices = append(indices, i)
		}

		sort.SliceStable(indices, func(i, j int) bool {
			ri := remainders[indices[i]]
			rj := remainders[indices[j]]
			switch {
			case ri.Equal(rj):
				return sortedParticipants[indices[i]].Address < sortedParticipants[indices[j]].Address
			default:
				return ri.GT(rj)
			}
		})

		for _, idx := range indices {
			if remaining == 0 {
				break
			}
			assigned[idx]++
			remaining--
		}
	}

	// 4. Final validation: slot counts should sum to totalSlots.
	checkTotal := int64(0)
	for _, cnt := range assigned {
		checkTotal += cnt
	}
	if checkTotal != int64(totalSlots) {
		return nil, fmt.Errorf("slot assignment mismatch: expected %d, got %d", totalSlots, checkTotal)
	}

	// Log the amount of non-zero voting power that got zero slots under strict weight allocation.
	nonZeroCount := 0
	excludedCount := 0
	excludedWeight := math.LegacyZeroDec()
	for i, p := range sortedParticipants {
		if p.PercentageWeight.IsZero() {
			continue
		}
		nonZeroCount++
		if assigned[i] == 0 {
			excludedCount++
			excludedWeight = excludedWeight.Add(p.PercentageWeight)
		}
	}
	if excludedCount > 0 {
		excludedPercentage := excludedWeight.Quo(totalWeight).Mul(math.LegacyNewDec(100))
		k.Logger().Warn(
			"Some non-zero-weight participants received zero slots under strict weight allocation",
			"non_zero_participant_count", nonZeroCount,
			"excluded_participant_count", excludedCount,
			"excluded_weight_percentage", excludedPercentage.String(),
			"total_slots", totalSlots,
		)
	}

	// 5. Build the BLS participant list with contiguous slot ranges.
	blsParticipants := make([]types.BLSParticipantInfo, 0, len(sortedParticipants))
	currentSlot := uint32(0)
	for i, participant := range sortedParticipants {
		slotCount := assigned[i]
		if slotCount <= 0 {
			continue
		}

		startIndex := currentSlot
		endIndex := startIndex + uint32(slotCount) - 1

		// Older versions clamped endIndex to totalSlots - 1 as a
		// defensive fallback. The assignment uses fixed-point decimals (LegacyDec),
		// not floating-point math. With the current checks (including checkTotal
		// above), this branch should be unreachable, so we fail fast instead of
		// masking a logic bug with silent clamping.
		if endIndex >= totalSlots {
			return nil, fmt.Errorf("slot assignment overflow: ending slot index %d exceeds total slots %d", endIndex, totalSlots)
		}

		blsParticipant := types.BLSParticipantInfo{
			Address:                    participant.Address,
			PercentageWeight:           participant.PercentageWeight,
			Secp256K1PublicKey:         participant.Secp256k1PublicKey,
			AllowedSecp256K1PublicKeys: participant.AllowedSecp256k1PublicKeys,
			SlotStartIndex:             startIndex,
			SlotEndIndex:               endIndex,
		}

		blsParticipants = append(blsParticipants, blsParticipant)
		currentSlot = endIndex + 1

		k.Logger().Debug(
			"Assigned slots to participant",
			"address", participant.Address,
			"weight", participant.PercentageWeight.String(),
			"slots", fmt.Sprintf("[%d, %d]", startIndex, endIndex),
			"slot_count", slotCount,
		)
	}

	// Verify all slots are assigned
	if currentSlot != totalSlots {
		return nil, fmt.Errorf("slot assignment error: assigned %d slots but expected %d", currentSlot, totalSlots)
	}

	return blsParticipants, nil
}

// SetEpochBLSData stores EpochBLSData in the state.
//
// DealerParts and VerificationSubmissions are stored out-of-band under
// per-participant sub-keys (see SetDealerPart, SetVerificationSubmission
// and their respective prefix/sub-key helpers). Any non-empty entries in
// the input struct are synced to sub-keys. The base struct is persisted
// with both fields zeroed so it stays constant-size. This means callers
// can set dealer parts or verification submissions via this function
// (e.g., during DKG initialization or in tests), and the data will be
// readable via GetEpochBLSData which rehydrates from sub-keys.
//
// The dealer HOT PATH (MsgSubmitDealerPart) bypasses this function
// entirely and calls SetDealerPart directly. The verifier HOT PATH
// (SubmitVerificationVector) similarly calls SetVerificationSubmission
// directly — a single sub-key write with constant gas cost regardless
// of how many other dealers/verifiers have already submitted.
func (k Keeper) SetEpochBLSData(ctx sdk.Context, epochBLSData types.EpochBLSData) error {
	store := k.storeService.OpenKVStore(ctx)

	// Sync any non-empty dealer parts to their sub-keys. Empty placeholders
	// (DealerAddress == "") are skipped — they only exist as in-memory
	// sentinels during DKG initialization.
	for i, dp := range epochBLSData.DealerParts {
		if dp != nil && dp.DealerAddress != "" {
			if err := k.SetDealerPart(ctx, epochBLSData.EpochId, uint32(i), dp); err != nil {
				return fmt.Errorf("sync dealer part %d to sub-key: %w", i, err)
			}
		}
	}

	// Sync any non-empty verification submissions to their sub-keys. Empty
	// placeholders (len(DealerValidity) == 0) are skipped — they are the
	// in-memory sentinels created in InitiateKeyGenerationForEpoch before
	// any verifier has submitted.
	for i, vs := range epochBLSData.VerificationSubmissions {
		if vs != nil && len(vs.DealerValidity) > 0 {
			if err := k.SetVerificationSubmission(ctx, epochBLSData.EpochId, uint32(i), vs); err != nil {
				return fmt.Errorf("sync verification submission %d to sub-key: %w", i, err)
			}
		}
	}

	// Sync any inline dealer complaints to their sub-keys. Callers that
	// pre-populate the slice (genesis import, tests) hit this path; the
	// hot-path verifier handler writes sub-keys directly via
	// SetDealerComplaint and passes DealerComplaints = nil here.
	for i := range epochBLSData.DealerComplaints {
		complaint := epochBLSData.DealerComplaints[i]
		if err := k.SetDealerComplaint(ctx, epochBLSData.EpochId, &complaint); err != nil {
			return fmt.Errorf("sync dealer complaint (%d,%d): %w", complaint.DealerIndex, complaint.ComplainerIndex, err)
		}
	}

	// Persist the base struct with the split-out fields zeroed so writes
	// stay constant-size. We copy to avoid mutating the caller's struct.
	baseCopy := epochBLSData
	baseCopy.DealerParts = nil
	baseCopy.VerificationSubmissions = nil
	baseCopy.DealerComplaints = nil

	key := types.EpochBLSDataKey(baseCopy.EpochId)
	value, err := k.cdc.Marshal(&baseCopy)
	if err != nil {
		return err
	}
	return store.Set(key, value)
}

// SetEpochBLSDataBaseOnly persists only the base-struct fields, skipping
// the per-entry sub-key re-sync that SetEpochBLSData normally performs for
// DealerParts, VerificationSubmissions, and DealerComplaints. Use when the
// caller is updating metadata (e.g. DkgPhase) on a rehydrated struct whose
// sub-key contents are unchanged.
//
// epochBLSData is taken by value; the caller's in-memory copy is untouched,
// so callers can keep using the fully-hydrated view (e.g. to emit events
// carrying the full EpochBLSData) after this returns.
func (k Keeper) SetEpochBLSDataBaseOnly(ctx sdk.Context, epochBLSData types.EpochBLSData) error {
	epochBLSData.DealerParts = nil
	epochBLSData.VerificationSubmissions = nil
	epochBLSData.DealerComplaints = nil
	return k.SetEpochBLSData(ctx, epochBLSData)
}

// SetDealerPart writes a single dealer part under its own sub-key. Hot path
// for MsgSubmitDealerPart; constant gas regardless of submission order.
func (k Keeper) SetDealerPart(ctx sdk.Context, epochID uint64, participantIndex uint32, dealerPart *types.DealerPartStorage) error {
	if dealerPart == nil {
		return fmt.Errorf("nil dealer part")
	}
	value, err := k.cdc.Marshal(dealerPart)
	if err != nil {
		return fmt.Errorf("marshal dealer part: %w", err)
	}
	k.dealerPartsStore(ctx, epochID).Set(types.DealerPartSubKey(participantIndex), value)
	return nil
}

// GetDealerPart reads a single dealer part for a participant. Returns
// (nil, nil) if no submission exists yet for that slot.
func (k Keeper) GetDealerPart(ctx sdk.Context, epochID uint64, participantIndex uint32) (*types.DealerPartStorage, error) {
	value := k.dealerPartsStore(ctx, epochID).Get(types.DealerPartSubKey(participantIndex))
	if value == nil {
		return nil, nil
	}
	var dp types.DealerPartStorage
	if err := k.cdc.Unmarshal(value, &dp); err != nil {
		return nil, err
	}
	return &dp, nil
}

// DeleteDealerPartsForEpoch removes every dealer part sub-key for an epoch.
// Called when an epoch's DKG state is being torn down so stale dealer parts
// don't accumulate in state. Not used on the normal phase-transition path —
// verifying phase still needs to read dealer parts.
func (k Keeper) DeleteDealerPartsForEpoch(ctx sdk.Context, epochID uint64) error {
	dealerStore := k.dealerPartsStore(ctx, epochID)
	it := dealerStore.Iterator(nil, nil)

	var keysToDelete [][]byte
	for ; it.Valid(); it.Next() {
		// Copy the key — the iterator's key is only valid until Next().
		keysToDelete = append(keysToDelete, append([]byte(nil), it.Key()...))
	}
	it.Close()

	for _, key := range keysToDelete {
		dealerStore.Delete(key)
	}
	return nil
}

// SetVerificationSubmission writes a single verification vector submission
// under its own sub-key. Cost is constant in the number of verifiers that
// have already submitted, so every verifier pays the same gas regardless of
// submission order. This is the hot path called by SubmitVerificationVector.
func (k Keeper) SetVerificationSubmission(ctx sdk.Context, epochID uint64, participantIndex uint32, submission *types.VerificationVectorSubmission) error {
	if submission == nil {
		return fmt.Errorf("nil verification submission")
	}
	value, err := k.cdc.Marshal(submission)
	if err != nil {
		return fmt.Errorf("marshal verification submission: %w", err)
	}
	k.verificationSubmissionsStore(ctx, epochID).Set(types.VerificationSubmissionSubKey(participantIndex), value)
	return nil
}

// GetVerificationSubmission reads a single verification vector submission for
// a participant. Returns (nil, nil) if no submission exists yet.
func (k Keeper) GetVerificationSubmission(ctx sdk.Context, epochID uint64, participantIndex uint32) (*types.VerificationVectorSubmission, error) {
	value := k.verificationSubmissionsStore(ctx, epochID).Get(types.VerificationSubmissionSubKey(participantIndex))
	if value == nil {
		return nil, nil
	}
	var vs types.VerificationVectorSubmission
	if err := k.cdc.Unmarshal(value, &vs); err != nil {
		return nil, err
	}
	return &vs, nil
}

// DeleteVerificationSubmissionsForEpoch removes every verification
// submission sub-key for an epoch. Mirrors DeleteDealerPartsForEpoch for
// epoch teardown.
func (k Keeper) DeleteVerificationSubmissionsForEpoch(ctx sdk.Context, epochID uint64) error {
	store := k.verificationSubmissionsStore(ctx, epochID)
	it := store.Iterator(nil, nil)

	var keysToDelete [][]byte
	for ; it.Valid(); it.Next() {
		keysToDelete = append(keysToDelete, append([]byte(nil), it.Key()...))
	}
	it.Close()

	for _, key := range keysToDelete {
		store.Delete(key)
	}
	return nil
}

// SetDealerComplaint writes a single dealer complaint under its own
// sub-key. Cost is constant in the number of complaints already recorded
// for the epoch, so every verifier's complaint writes cost the same gas
// regardless of submission order.
func (k Keeper) SetDealerComplaint(ctx sdk.Context, epochID uint64, complaint *types.DealerComplaint) error {
	if complaint == nil {
		return fmt.Errorf("nil dealer complaint")
	}
	value, err := k.cdc.Marshal(complaint)
	if err != nil {
		return fmt.Errorf("marshal dealer complaint: %w", err)
	}
	k.dealerComplaintsStore(ctx, epochID).Set(
		types.DealerComplaintSubKey(complaint.DealerIndex, complaint.ComplainerIndex),
		value,
	)
	return nil
}

// GetDealerComplaint reads a single dealer complaint by its (dealer,
// complainer) compound key. Returns (nil, nil) if no such complaint
// exists.
func (k Keeper) GetDealerComplaint(ctx sdk.Context, epochID uint64, dealerIndex, complainerIndex uint32) (*types.DealerComplaint, error) {
	value := k.dealerComplaintsStore(ctx, epochID).Get(types.DealerComplaintSubKey(dealerIndex, complainerIndex))
	if value == nil {
		return nil, nil
	}
	var c types.DealerComplaint
	if err := k.cdc.Unmarshal(value, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// HasDealerComplaint reports whether a complaint exists for the given
// (dealer, complainer) pair without unmarshaling the value.
func (k Keeper) HasDealerComplaint(ctx sdk.Context, epochID uint64, dealerIndex, complainerIndex uint32) bool {
	return k.dealerComplaintsStore(ctx, epochID).Has(types.DealerComplaintSubKey(dealerIndex, complainerIndex))
}

// ListDealerComplaintsForEpoch returns every dealer complaint recorded for
// an epoch, in ascending (dealer_index, complainer_index) order. Used by
// GetEpochBLSData's rehydration path and by phase-transition / dispute
// paths that need the full set.
func (k Keeper) ListDealerComplaintsForEpoch(ctx sdk.Context, epochID uint64) ([]types.DealerComplaint, error) {
	it := k.dealerComplaintsStore(ctx, epochID).Iterator(nil, nil)
	defer it.Close()

	var out []types.DealerComplaint
	for ; it.Valid(); it.Next() {
		var c types.DealerComplaint
		if err := k.cdc.Unmarshal(it.Value(), &c); err != nil {
			return nil, fmt.Errorf("unmarshal dealer complaint: %w", err)
		}
		out = append(out, c)
	}
	return out, nil
}

// DeleteDealerComplaint removes a single (dealer, complainer) sub-key.
// Used by phase transition when filtering out complaints against dealers
// that failed candidacy.
func (k Keeper) DeleteDealerComplaint(ctx sdk.Context, epochID uint64, dealerIndex, complainerIndex uint32) {
	k.dealerComplaintsStore(ctx, epochID).Delete(types.DealerComplaintSubKey(dealerIndex, complainerIndex))
}

// DeleteDealerComplaintsForEpoch removes every dealer complaint sub-key
// for an epoch. Mirrors the other epoch-teardown helpers.
func (k Keeper) DeleteDealerComplaintsForEpoch(ctx sdk.Context, epochID uint64) error {
	store := k.dealerComplaintsStore(ctx, epochID)
	it := store.Iterator(nil, nil)

	var keysToDelete [][]byte
	for ; it.Valid(); it.Next() {
		keysToDelete = append(keysToDelete, append([]byte(nil), it.Key()...))
	}
	it.Close()

	for _, key := range keysToDelete {
		store.Delete(key)
	}
	return nil
}

// GetEpochBLSData retrieves EpochBLSData from the state. DealerParts and
// VerificationSubmissions are rehydrated from per-participant sub-keys so
// callers see the same shape they always have (slices indexed by
// participant index, with empty-value placeholders for slots that have not
// yet submitted).
//
// Backward compatibility: if the base struct still has either field inline
// (e.g. an EpochBLSData written by a pre-split handler), those values are
// used as the baseline and any sub-key entries take precedence. This lets
// the split take effect immediately after upgrade without a migration
// step.
func (k Keeper) GetEpochBLSData(ctx sdk.Context, epochID uint64) (types.EpochBLSData, error) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.EpochBLSDataKey(epochID)

	value, err := store.Get(key)
	if err != nil {
		return types.EpochBLSData{}, err
	}
	if value == nil {
		return types.EpochBLSData{}, types.ErrEpochBLSDataNotFound
	}

	var epochBLSData types.EpochBLSData
	if err := k.cdc.Unmarshal(value, &epochBLSData); err != nil {
		return types.EpochBLSData{}, err
	}

	numParticipants := len(epochBLSData.Participants)

	// Ensure DealerParts has an entry per participant. If the base struct
	// stored inline dealer parts (legacy writes), those serve as the
	// starting point; otherwise we initialize empty placeholders so callers
	// that index by participant position still work.
	if len(epochBLSData.DealerParts) < numParticipants {
		expanded := make([]*types.DealerPartStorage, numParticipants)
		for i := range expanded {
			if i < len(epochBLSData.DealerParts) && epochBLSData.DealerParts[i] != nil {
				expanded[i] = epochBLSData.DealerParts[i]
			} else {
				expanded[i] = &types.DealerPartStorage{}
			}
		}
		epochBLSData.DealerParts = expanded
	}

	// Same expansion for VerificationSubmissions. Placeholders have an
	// empty DealerValidity slice, matching the sentinels created in
	// InitiateKeyGenerationForEpoch.
	if len(epochBLSData.VerificationSubmissions) < numParticipants {
		expanded := make([]*types.VerificationVectorSubmission, numParticipants)
		for i := range expanded {
			if i < len(epochBLSData.VerificationSubmissions) && epochBLSData.VerificationSubmissions[i] != nil {
				expanded[i] = epochBLSData.VerificationSubmissions[i]
			} else {
				expanded[i] = &types.VerificationVectorSubmission{DealerValidity: []bool{}}
			}
		}
		epochBLSData.VerificationSubmissions = expanded
	}

	// Overlay sub-key dealer parts on top of the base slice with a single
	// prefix scan. Any participant whose sub-key entry exists takes
	// precedence over whatever was inlined. Sub-keys for participant
	// indices outside the current participant set are ignored (stale data
	// should have been cleared via DeleteDealerPartsForEpoch).
	if err := rehydrateFromSubKeys(
		k.dealerPartsStore(ctx, epochID),
		numParticipants,
		types.ParseDealerPartSubKey,
		func(idx uint32, value []byte) error {
			var dp types.DealerPartStorage
			if err := k.cdc.Unmarshal(value, &dp); err != nil {
				return fmt.Errorf("unmarshal dealer part %d: %w", idx, err)
			}
			epochBLSData.DealerParts[idx] = &dp
			return nil
		},
	); err != nil {
		return types.EpochBLSData{}, err
	}

	// Same overlay for verification submissions.
	if err := rehydrateFromSubKeys(
		k.verificationSubmissionsStore(ctx, epochID),
		numParticipants,
		types.ParseVerificationSubmissionSubKey,
		func(idx uint32, value []byte) error {
			var vs types.VerificationVectorSubmission
			if err := k.cdc.Unmarshal(value, &vs); err != nil {
				return fmt.Errorf("unmarshal verification submission %d: %w", idx, err)
			}
			epochBLSData.VerificationSubmissions[idx] = &vs
			return nil
		},
	); err != nil {
		return types.EpochBLSData{}, err
	}

	// Dealer complaints rehydrate differently from the two index-positioned
	// slices above: complaints are sparse (one per real (dealer, complainer)
	// pair), so we append to an initially-empty slice in sub-key order
	// rather than placing into fixed indices. Legacy inline entries in the
	// base struct serve as the baseline; sub-key entries overlay on top
	// when they share the same (dealer, complainer) pair.
	legacyByPair := make(map[uint64]int, len(epochBLSData.DealerComplaints))
	for i, c := range epochBLSData.DealerComplaints {
		legacyByPair[dealerComplaintPairKey(c.DealerIndex, c.ComplainerIndex)] = i
	}
	cIt := k.dealerComplaintsStore(ctx, epochID).Iterator(nil, nil)
	defer cIt.Close()
	for ; cIt.Valid(); cIt.Next() {
		dealerIdx, complainerIdx, err := types.ParseDealerComplaintSubKey(cIt.Key())
		if err != nil {
			return types.EpochBLSData{}, fmt.Errorf("parse dealer complaint sub-key: %w", err)
		}
		var c types.DealerComplaint
		if err := k.cdc.Unmarshal(cIt.Value(), &c); err != nil {
			return types.EpochBLSData{}, fmt.Errorf("unmarshal dealer complaint (%d,%d): %w", dealerIdx, complainerIdx, err)
		}
		if existingIdx, ok := legacyByPair[dealerComplaintPairKey(dealerIdx, complainerIdx)]; ok {
			epochBLSData.DealerComplaints[existingIdx] = c
			continue
		}
		epochBLSData.DealerComplaints = append(epochBLSData.DealerComplaints, c)
	}

	return epochBLSData, nil
}

// dealerComplaintPairKey packs a (dealer, complainer) pair into a single
// uint64 so it can be used as a map key for fast deduplication during
// rehydration.
func dealerComplaintPairKey(dealerIdx, complainerIdx uint32) uint64 {
	return (uint64(dealerIdx) << 32) | uint64(complainerIdx)
}

// rehydrateFromSubKeys scans every entry in store and, for each sub-key whose
// parsed participant index is within the current participant count, decodes
// the value via the supplied apply callback. Sub-keys outside the participant
// range are skipped (stale data expected to have been cleared). Close on the
// underlying iterator is guaranteed even if apply returns an error.
func rehydrateFromSubKeys(
	store prefix.Store,
	numParticipants int,
	parseKey func([]byte) (uint32, error),
	apply func(idx uint32, value []byte) error,
) error {
	it := store.Iterator(nil, nil)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		idx, err := parseKey(it.Key())
		if err != nil {
			return fmt.Errorf("parse sub-key: %w", err)
		}
		if int(idx) >= numParticipants {
			continue
		}
		if err := apply(idx, it.Value()); err != nil {
			return err
		}
	}
	return nil
}
