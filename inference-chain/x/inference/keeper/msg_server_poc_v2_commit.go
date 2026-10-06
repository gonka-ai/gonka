package keeper

import (
	"context"
	"fmt"
	"slices"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

// pocV2MaxTreeDepth is the maximum SMST depth a PoC v2 commit may bind.
// Matches decentralized-api/poc/artifacts.smstMaxDepth.
const pocV2MaxTreeDepth uint32 = 32

type pocV2CommitUpdate struct {
	modelID    string
	entry      *types.PoCV2CommitEntry
	countDelta uint64
}

// PoCV2StoreCommit handles submission of off-chain artifact store commits.
func (k msgServer) PoCV2StoreCommit(goCtx context.Context, msg *types.MsgPoCV2StoreCommit) (*types.MsgPoCV2StoreCommitResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Check for active confirmation PoC event
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[PoCV2StoreCommit] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	// Decode before the blocklist check. Membership is account bytes, and the
	// stored participant string is the canonical spelling.
	addr, err := decodePoCCreator(msg.Creator)
	if err != nil {
		return nil, err
	}
	if k.IsPoCParticipantBlocked(ctx, addr.String()) {
		k.LogError(PocFailureTag+"[PoCV2StoreCommit] participant is blocked from PoC", types.PoC, "participant", addr.String())
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, addr.String())
	}

	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}

	// Validate PoC window
	// For confirmation PoC: accept during batch submission window (generation + exchange)
	// For regular PoC: accept during exchange window
	if isActive && activeEvent != nil && startBlockHeight == activeEvent.TriggerHeight {
		epochParams := params.EpochParams
		if !activeEvent.IsInBatchSubmissionWindow(currentBlockHeight, epochParams) {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC batch submission window closed")
		}
		if err := k.CheckEpochModelCommit(ctx, activeEvent.EpochIndex, addr, msg.Entries); err != nil {
			return nil, err
		}
	} else {
		epochParams := params.EpochParams
		upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
		if !found {
			return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "failed to get upcoming epoch")
		}
		epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

		if !epochContext.IsStartOfPocStage(startBlockHeight) {
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
				fmt.Sprintf("start block height %d doesn't match PoC stage start %d", startBlockHeight, epochContext.PocStartBlockHeight))
		}
		if !epochContext.IsPoCExchangeWindow(currentBlockHeight) {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "PoC exchange window closed")
		}
	}

	existingByModel, err := k.loadExistingPoCV2StoreCommits(ctx, startBlockHeight, addr)
	if err != nil {
		return nil, err
	}

	updates, totalCountDelta, err := k.buildPoCV2CommitUpdates(ctx, currentBlockHeight, msg.Entries, existingByModel)
	if err != nil {
		return nil, err
	}

	if err := k.ChargeExtraGas(ctx, addr, msg, totalCountDelta, len(existingByModel) == 0); err != nil {
		return nil, err
	}

	if err := k.persistPoCV2CommitUpdates(ctx, addr.String(), startBlockHeight, currentBlockHeight, addr, updates); err != nil {
		return nil, err
	}

	return &types.MsgPoCV2StoreCommitResponse{}, nil
}

func (k Keeper) CheckEpochModelCommit(
	ctx context.Context,
	epochIndex uint64,
	participant sdk.AccAddress,
	entries []*types.PoCV2CommitEntry,
) error {
	participants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("active participants not found for epoch %d", epochIndex))
	}
	index := participant.String()
	var epochModels []string
	member := false
	for _, p := range participants.Participants {
		if p != nil && p.Index == index {
			epochModels = p.Models
			member = true
			break
		}
	}
	if !member {
		return sdkerrors.Wrap(types.ErrParticipantNotFound, fmt.Sprintf("%s is not an active participant of epoch %d", index, epochIndex))
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if !slices.Contains(epochModels, entry.ModelId) {
			return sdkerrors.Wrap(types.ErrInvalidModel, fmt.Sprintf("model_id %q is not one of the participant's epoch models", entry.ModelId))
		}
	}
	return nil
}

func (k msgServer) loadExistingPoCV2StoreCommits(
	ctx context.Context,
	startBlockHeight int64,
	addr sdk.AccAddress,
) (map[string]types.PoCV2StoreCommit, error) {
	existingByModel := make(map[string]types.PoCV2StoreCommit)

	iter, err := k.PoCV2StoreCommits.Iterate(ctx, collections.NewSuperPrefixedTripleRange[int64, sdk.AccAddress, string](startBlockHeight, addr))
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to iterate existing commits: %v", err))
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, keyErr := iter.Key()
		if keyErr != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to read existing commit key: %v", keyErr))
		}
		value, valueErr := iter.Value()
		if valueErr != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to read existing commit: %v", valueErr))
		}
		existingByModel[key.K3()] = restoredPoCV2StoreCommit(key, value)
	}

	return existingByModel, nil
}

func (k msgServer) buildPoCV2CommitUpdates(
	ctx context.Context,
	currentBlockHeight int64,
	entries []*types.PoCV2CommitEntry,
	existingByModel map[string]types.PoCV2StoreCommit,
) ([]pocV2CommitUpdate, uint64, error) {
	updates := make([]pocV2CommitUpdate, 0, len(entries))
	seenModelIDs := make(map[string]struct{}, len(entries))
	var totalCountDelta uint64

	for _, entry := range entries {
		if entry != nil && entry.ModelId != "" {
			if _, dup := seenModelIDs[entry.ModelId]; dup {
				return nil, 0, sdkerrors.Wrap(
					types.ErrIllegalState,
					fmt.Sprintf("duplicate model_id %q in one commit", entry.ModelId),
				)
			}
			seenModelIDs[entry.ModelId] = struct{}{}
		}
		update, err := k.buildPoCV2CommitUpdate(ctx, currentBlockHeight, existingByModel, entry)
		if err != nil {
			return nil, 0, err
		}
		if totalCountDelta+update.countDelta < totalCountDelta {
			return nil, 0, sdkerrors.Wrap(types.ErrIllegalState, "total count delta overflow")
		}
		totalCountDelta += update.countDelta
		updates = append(updates, update)
	}

	return updates, totalCountDelta, nil
}

func (k msgServer) buildPoCV2CommitUpdate(
	ctx context.Context,
	currentBlockHeight int64,
	existingByModel map[string]types.PoCV2StoreCommit,
	entry *types.PoCV2CommitEntry,
) (pocV2CommitUpdate, error) {
	if entry.Count == 0 {
		return pocV2CommitUpdate{}, sdkerrors.Wrap(types.ErrIllegalState, "entry count must be greater than 0")
	}
	if len(entry.RootHash) != 32 {
		return pocV2CommitUpdate{}, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("root_hash must be 32 bytes, got %d", len(entry.RootHash)))
	}
	if entry.TreeDepth < 1 || entry.TreeDepth > pocV2MaxTreeDepth {
		return pocV2CommitUpdate{}, sdkerrors.Wrap(
			types.ErrIllegalState,
			fmt.Sprintf("tree_depth must be in 1..%d, got %d", pocV2MaxTreeDepth, entry.TreeDepth),
		)
	}

	modelID := entry.ModelId
	if modelID == "" {
		return pocV2CommitUpdate{}, sdkerrors.Wrap(types.ErrIllegalState, "model_id must not be empty")
	}
	if !k.IsValidGovernanceModel(ctx, modelID) {
		return pocV2CommitUpdate{}, sdkerrors.Wrap(types.ErrInvalidModel, fmt.Sprintf("model_id %q is not a governance model", modelID))
	}

	countDelta := uint64(entry.Count)
	if existing, found := existingByModel[modelID]; found {
		if existing.CommitBlockHeight == currentBlockHeight {
			return pocV2CommitUpdate{}, sdkerrors.Wrap(types.ErrIllegalState, "only one commit per block allowed")
		}
		if entry.Count <= existing.Count {
			return pocV2CommitUpdate{}, sdkerrors.Wrap(
				types.ErrIllegalState,
				fmt.Sprintf("count must increase: got %d, last recorded %d", entry.Count, existing.Count),
			)
		}
		if entry.TreeDepth != existing.TreeDepth {
			return pocV2CommitUpdate{}, sdkerrors.Wrap(
				types.ErrIllegalState,
				fmt.Sprintf("tree_depth must stay %d, got %d", existing.TreeDepth, entry.TreeDepth),
			)
		}
		countDelta = uint64(entry.Count - existing.Count)
	}

	return pocV2CommitUpdate{
		modelID:    modelID,
		entry:      entry,
		countDelta: countDelta,
	}, nil
}

func (k msgServer) persistPoCV2CommitUpdates(
	ctx context.Context,
	creator string,
	startBlockHeight int64,
	currentBlockHeight int64,
	addr sdk.AccAddress,
	updates []pocV2CommitUpdate,
) error {
	for _, update := range updates {
		pk := pocV2StoreCommitKey(startBlockHeight, addr, update.modelID)
		commit := types.PoCV2StoreCommit{
			ParticipantAddress:       creator,
			PocStageStartBlockHeight: startBlockHeight,
			Count:                    update.entry.Count,
			RootHash:                 update.entry.RootHash,
			CommitBlockHeight:        currentBlockHeight,
			ModelId:                  update.modelID,
			TreeDepth:                update.entry.TreeDepth,
		}

		if err := k.PoCV2StoreCommits.Set(ctx, pk, storedPoCV2StoreCommit(commit, addr)); err != nil {
			return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store commit: %v", err))
		}

		k.LogInfo("[PoCV2StoreCommit] Stored", types.PoC,
			"participant", creator,
			"model_id", update.modelID,
			"startBlockHeight", startBlockHeight,
			"count", update.entry.Count)
	}

	return nil
}

// checkedMul returns a*b and true if the multiplication overflows uint64.
func checkedMul(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, false
	}
	result := a * b
	if result/a != b {
		return 0, true
	}
	return result, false
}

// MLNodeWeightDistribution handles submission of per-node weight distribution.
func (k msgServer) MLNodeWeightDistribution(goCtx context.Context, msg *types.MsgMLNodeWeightDistribution) (*types.MsgMLNodeWeightDistributionResponse, error) {
	// Bound the decoded message before permission, window, or store lookups.
	if err := msg.ValidateRawBounds(); err != nil {
		return nil, err
	}
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Check for active confirmation PoC event
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[MLNodeWeightDistribution] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	addr, err := decodePoCCreator(msg.Creator)
	if err != nil {
		return nil, err
	}
	if k.IsPoCParticipantBlocked(ctx, addr.String()) {
		k.LogError(PocFailureTag+"[MLNodeWeightDistribution] participant is blocked from PoC", types.PoC, "participant", addr.String())
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, addr.String())
	}

	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}

	// Validate window: accept from exchange window through end of validation
	if isActive && activeEvent != nil {
		if startBlockHeight != activeEvent.TriggerHeight {
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
				fmt.Sprintf("confirmation PoC: start block height %d doesn't match event trigger %d", startBlockHeight, activeEvent.TriggerHeight))
		}
		epochParams := params.EpochParams
		validationEnd := activeEvent.GetValidationEnd(epochParams)
		if currentBlockHeight > validationEnd {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC validation window closed")
		}
	} else {
		epochParams := params.EpochParams
		upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
		if !found {
			return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "failed to get upcoming epoch")
		}
		epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

		if !epochContext.IsStartOfPocStage(startBlockHeight) {
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
				fmt.Sprintf("start block height %d doesn't match PoC stage start %d", startBlockHeight, epochContext.PocStartBlockHeight))
		}
		// Accept through end of validation phase
		validationEnd := epochContext.EndOfPoCValidation()
		if currentBlockHeight > validationEnd {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "PoC validation window closed")
		}
	}

	// Canonicalize every entry before any write. A later failure must not
	// leave an earlier model from this transaction stored.
	canonical, err := k.CanonicalMLNodeWeightDistribution(ctx, startBlockHeight, addr, msg.Entries)
	if err != nil {
		return nil, err
	}

	for _, entry := range canonical {
		pk := pocV2StoreCommitKey(startBlockHeight, addr, entry.ModelId)
		distribution := types.MLNodeWeightDistribution{
			ParticipantAddress:       addr.String(),
			PocStageStartBlockHeight: startBlockHeight,
			Weights:                  entry.Weights,
			ModelId:                  entry.ModelId,
		}

		if err := k.MLNodeWeightDistributions.Set(ctx, pk, storedMLNodeWeightDistribution(distribution, addr)); err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store distribution: %v", err))
		}

		k.LogInfo("[MLNodeWeightDistribution] Stored", types.PoC,
			"participant", addr.String(),
			"model_id", entry.ModelId,
			"startBlockHeight", startBlockHeight,
			"nodeCount", len(entry.Weights))
	}

	return &types.MsgMLNodeWeightDistributionResponse{}, nil
}

// CanonicalMLNodeWeightDistribution checks structural shape, governance
// models, store commits, the positive-row limit, and the commit sum. It
// returns the positive rows to store and does not read hardware inventory.
// The decoded message is not modified.
func (k Keeper) CanonicalMLNodeWeightDistribution(
	ctx context.Context,
	startBlockHeight int64,
	participant sdk.AccAddress,
	entries []*types.MLNodeDistributionEntry,
) ([]*types.MLNodeDistributionEntry, error) {
	canonical, err := types.CanonicalMLNodeWeightEntries(entries)
	if err != nil {
		return nil, err
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	limit := params.GetEpochValidationParams().PositiveDistributionRowLimit()
	for _, entry := range canonical {
		if !k.IsValidGovernanceModel(ctx, entry.ModelId) {
			return nil, sdkerrors.Wrap(types.ErrInvalidModel, fmt.Sprintf("model_id %q is not a governance model", entry.ModelId))
		}
		if len(entry.Weights) > limit {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf(
				"too many positive weight rows for model %q: %d > %d",
				entry.ModelId, len(entry.Weights), limit,
			))
		}
		pk := pocV2StoreCommitKey(startBlockHeight, participant, entry.ModelId)
		commit, err := k.PoCV2StoreCommits.Get(ctx, pk)
		if err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "no store commit found for this stage and model")
		}
		sum, err := sumMLNodeWeights(entry.Weights)
		if err != nil {
			return nil, err
		}
		if sum != uint64(commit.Count) {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("weight sum %d does not match committed count %d", sum, commit.Count))
		}
	}
	return canonical, nil
}

func sumMLNodeWeights(weights []*types.MLNodeWeight) (uint64, error) {
	var sum uint64
	for _, weight := range weights {
		if weight == nil {
			return 0, sdkerrors.Wrap(types.ErrIllegalState, "nil weight row")
		}
		next := sum + uint64(weight.Weight)
		if next < sum {
			return 0, sdkerrors.Wrap(types.ErrIllegalState, "weight sum overflow")
		}
		sum = next
	}
	return sum, nil
}
