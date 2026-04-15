package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

type pocV2CommitUpdate struct {
	modelID    string
	entry      *types.PoCV2CommitEntry
	countDelta uint64
}

// PoCV2StoreCommit handles submission of off-chain artifact store commits.
func (k msgServer) PoCV2StoreCommit(goCtx context.Context, msg *types.MsgPoCV2StoreCommit) (*types.MsgPoCV2StoreCommitResponse, error) {
	if err := k.CheckPermission(goCtx, msg, NoPermission); err != nil {
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

	// Participant access gating: blocklisted accounts cannot submit PoC artifacts.
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[PoCV2StoreCommit] participant is blocked from PoC", types.PoC, "participant", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
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

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	existingByModel, err := k.loadExistingPoCV2StoreCommits(ctx, startBlockHeight, addr)
	if err != nil {
		return nil, err
	}

	updates, totalCountDelta, err := k.buildPoCV2CommitUpdates(ctx, currentBlockHeight, msg.Entries, existingByModel)
	if err != nil {
		return nil, err
	}

	if err := chargePoCV2StoreCommitGas(ctx, params.FeeParams, len(existingByModel) == 0, totalCountDelta); err != nil {
		return nil, err
	}

	if err := k.persistPoCV2CommitUpdates(ctx, msg.Creator, startBlockHeight, currentBlockHeight, addr, updates); err != nil {
		return nil, err
	}

	return &types.MsgPoCV2StoreCommitResponse{}, nil
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
		existingByModel[key.K3()] = value
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
	var totalCountDelta uint64

	for _, entry := range entries {
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

	modelID := entry.ModelId
	if modelID == "" {
		return pocV2CommitUpdate{}, sdkerrors.Wrap(types.ErrIllegalState, "model_id must not be empty")
	}
	if _, found := k.GetGovernanceModel(ctx, modelID); !found {
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
		countDelta = uint64(entry.Count - existing.Count)
	}

	return pocV2CommitUpdate{
		modelID:    modelID,
		entry:      entry,
		countDelta: countDelta,
	}, nil
}

func chargePoCV2StoreCommitGas(
	ctx sdk.Context,
	feeParams *types.FeeParams,
	isFirstCommit bool,
	totalCountDelta uint64,
) error {
	if feeParams == nil {
		return nil
	}

	// Base validation gas is charged once per participant/stage.
	if isFirstCommit {
		ctx.GasMeter().ConsumeGas(storetypes.Gas(feeParams.BaseValidationGas), "poc_validation_base")
	}

	// Count gas is charged from the sum of per-model Count deltas.
	countGas, overflow := checkedMul(totalCountDelta, feeParams.GasPerPocCount)
	if overflow {
		return sdkerrors.Wrap(types.ErrIllegalState, "total_count_delta * gas_per_poc_count overflow")
	}
	ctx.GasMeter().ConsumeGas(storetypes.Gas(countGas), "poc_commit_count_delta")
	return nil
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
		}

		if err := k.PoCV2StoreCommits.Set(ctx, pk, commit); err != nil {
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
	if err := k.CheckPermission(goCtx, msg, NoPermission); err != nil {
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

	// Participant access gating: blocklisted accounts cannot submit PoC artifacts.
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[MLNodeWeightDistribution] participant is blocked from PoC", types.PoC, "participant", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
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
		confirmParams, err := k.GetParams(ctx)
		if err != nil {
			return nil, err
		}
		epochParams := confirmParams.EpochParams
		validationEnd := activeEvent.GetValidationEnd(epochParams)
		if currentBlockHeight > validationEnd {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC validation window closed")
		}
	} else {
		regularParams, err := k.Keeper.GetParams(goCtx)
		if err != nil {
			return nil, err
		}
		epochParams := regularParams.EpochParams
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

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	for _, entry := range msg.Entries {
		if len(entry.Weights) == 0 {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "entry weights must not be empty")
		}

		modelID := entry.ModelId
		if modelID == "" {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "model_id must not be empty")
		}
		if _, found := k.GetGovernanceModel(ctx, modelID); !found {
			return nil, sdkerrors.Wrap(types.ErrInvalidModel, fmt.Sprintf("model_id %q is not a governance model", modelID))
		}

		pk := pocV2StoreCommitKey(startBlockHeight, addr, modelID)
		commit, err := k.PoCV2StoreCommits.Get(ctx, pk)
		if err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "no store commit found for this stage and model")
		}

		var sum uint64
		for _, w := range entry.Weights {
			sum += uint64(w.Weight)
		}
		if sum != uint64(commit.Count) {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("weight sum %d does not match committed count %d", sum, commit.Count))
		}

		distribution := types.MLNodeWeightDistribution{
			ParticipantAddress:       msg.Creator,
			PocStageStartBlockHeight: startBlockHeight,
			Weights:                  entry.Weights,
			ModelId:                  modelID,
		}

		if err := k.MLNodeWeightDistributions.Set(ctx, pk, distribution); err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store distribution: %v", err))
		}

		k.LogInfo("[MLNodeWeightDistribution] Stored", types.PoC,
			"participant", msg.Creator,
			"model_id", modelID,
			"startBlockHeight", startBlockHeight,
			"nodeCount", len(entry.Weights))
	}

	return &types.MsgMLNodeWeightDistributionResponse{}, nil
}
