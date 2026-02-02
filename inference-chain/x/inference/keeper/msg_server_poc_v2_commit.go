package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// PoCV2StoreCommit handles submission of off-chain artifact store commits.
func (k msgServer) PoCV2StoreCommit(goCtx context.Context, msg *types.MsgPoCV2StoreCommit) (*types.MsgPoCV2StoreCommitResponse, error) {
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

	isMigrationTracking := params.PocParams.ConfirmationPocV2Enabled && isActive && activeEvent != nil && activeEvent.EventSequence == 0
	if !params.PocParams.PocV2Enabled && !isMigrationTracking {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	// Validate count and root_hash
	if msg.Count == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "count must be greater than 0")
	}
	if len(msg.RootHash) != 32 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("root_hash must be 32 bytes, got %d", len(msg.RootHash)))
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

	// Check existing commit for rate limit and count increase
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}
	pk := collections.Join(startBlockHeight, addr)
	existing, err := k.PoCV2StoreCommits.Get(ctx, pk)
	if err == nil {
		// Same-block rate limit: only one commit per block allowed
		if existing.CommitBlockHeight == currentBlockHeight {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "only one commit per block allowed")
		}
		// Strict count increase: new count must be greater than previous
		if msg.Count <= existing.Count {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("count must increase: got %d, last recorded %d", msg.Count, existing.Count))
		}
	}

	// Store commit with block height
	commit := types.PoCV2StoreCommit{
		ParticipantAddress:       msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		Count:                    msg.Count,
		RootHash:                 msg.RootHash,
		CommitBlockHeight:        currentBlockHeight,
	}

	if err := k.PoCV2StoreCommits.Set(ctx, pk, commit); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store commit: %v", err))
	}

	k.LogInfo("[PoCV2StoreCommit] Stored", types.PoC,
		"participant", msg.Creator,
		"startBlockHeight", startBlockHeight,
		"count", msg.Count)

	return &types.MsgPoCV2StoreCommitResponse{}, nil
}

// MLNodeWeightDistribution handles submission of per-node weight distribution.
func (k msgServer) MLNodeWeightDistribution(goCtx context.Context, msg *types.MsgMLNodeWeightDistribution) (*types.MsgMLNodeWeightDistributionResponse, error) {
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

	isMigrationTracking := params.PocParams.ConfirmationPocV2Enabled && isActive && activeEvent != nil && activeEvent.EventSequence == 0
	if !params.PocParams.PocV2Enabled && !isMigrationTracking {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	if len(msg.Weights) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "weights must not be empty")
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

	// Validate weight sum matches committed count
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}
	pk := collections.Join(startBlockHeight, addr)
	commit, err := k.PoCV2StoreCommits.Get(ctx, pk)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "no store commit found for this stage")
	}

	var sum uint64
	for _, w := range msg.Weights {
		sum += uint64(w.Weight)
	}
	if sum != uint64(commit.Count) {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("weight sum %d does not match committed count %d", sum, commit.Count))
	}

	// Store distribution
	distribution := types.MLNodeWeightDistribution{
		ParticipantAddress:       msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		Weights:                  msg.Weights,
	}

	if err := k.MLNodeWeightDistributions.Set(ctx, pk, distribution); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store distribution: %v", err))
	}

	k.LogInfo("[MLNodeWeightDistribution] Stored", types.PoC,
		"participant", msg.Creator,
		"startBlockHeight", startBlockHeight,
		"nodeCount", len(msg.Weights))

	return &types.MsgMLNodeWeightDistributionResponse{}, nil
}
