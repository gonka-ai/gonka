package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreatePoCChallenge(goCtx context.Context, msg *types.MsgCreatePoCChallenge) (*types.MsgCreatePoCChallengeResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	return k.Keeper.CreatePoCChallenge(goCtx, msg)
}

func (k msgServer) PoCChallengeStoreCommit(goCtx context.Context, msg *types.MsgPoCChallengeStoreCommit) (*types.MsgPoCChallengeStoreCommitResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}
	ch, found, err := k.GetPoCChallenge(goCtx, msg.Creator)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "no open challenge for signer")
	}
	if ch.FailureKind != types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_UNSET {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "challenge already decided")
	}
	epochIndex, ok := k.GetEffectiveEpochIndex(goCtx)
	if !ok || ch.EpochIndex != epochIndex {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "challenge is not for the current epoch")
	}
	if msg.PocStageStartBlockHeight != ch.StartHeight {
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, "start height is not current")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	height := ctx.BlockHeight()
	finish, err := k.ChallengeFinish(goCtx, ch)
	if err != nil {
		return nil, err
	}
	if height >= finish {
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, "challenge commit window closed")
	}

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}
	existingByModel, err := k.loadExistingChallengeCommits(goCtx, addr)
	if err != nil {
		return nil, err
	}
	updates, _, err := k.buildPoCV2CommitUpdates(goCtx, height, msg.Entries, existingByModel)
	if err != nil {
		return nil, err
	}
	// Confirmation-only: no extra gas like MsgPoCV2StoreCommit.
	if err := k.persistChallengeCommitUpdates(goCtx, msg.Creator, ch.StartHeight, height, addr, updates); err != nil {
		return nil, err
	}
	return &types.MsgPoCChallengeStoreCommitResponse{}, nil
}

func (k msgServer) loadExistingChallengeCommits(
	ctx context.Context,
	addr sdk.AccAddress,
) (map[string]types.PoCV2StoreCommit, error) {
	existingByModel := make(map[string]types.PoCV2StoreCommit)
	iter, err := k.PoCChallengeCommits.Iterate(ctx, collections.NewPrefixedPairRange[sdk.AccAddress, string](addr))
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
		existingByModel[key.K2()] = value
	}
	return existingByModel, nil
}

func (k msgServer) persistChallengeCommitUpdates(
	ctx context.Context,
	creator string,
	startBlockHeight int64,
	currentBlockHeight int64,
	addr sdk.AccAddress,
	updates []pocV2CommitUpdate,
) error {
	for _, update := range updates {
		commit := types.PoCV2StoreCommit{
			ParticipantAddress:       creator,
			PocStageStartBlockHeight: startBlockHeight,
			Count:                    update.entry.Count,
			RootHash:                 update.entry.RootHash,
			CommitBlockHeight:        currentBlockHeight,
			ModelId:                  update.modelID,
		}
		if err := k.PoCChallengeCommits.Set(ctx, collections.Join(addr, update.modelID), commit); err != nil {
			return sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store commit: %v", err))
		}
		k.LogInfo("[PoCChallengeStoreCommit] Stored", types.PoC,
			"participant", creator,
			"model_id", update.modelID,
			"startBlockHeight", startBlockHeight,
			"count", update.entry.Count)
	}
	return nil
}

func (k msgServer) SubmitPoCChallengeValidations(goCtx context.Context, msg *types.MsgSubmitPoCChallengeValidations) (*types.MsgSubmitPoCChallengeValidationsResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}
	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}
	if k.IsPoCParticipantBlocked(goCtx, msg.Creator) {
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	height := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight
	if err := k.validateChallengeVoteWindow(goCtx, height, startBlockHeight, params); err != nil {
		return nil, err
	}
	epochIndex, ok := k.GetEffectiveEpochIndex(goCtx)
	if !ok {
		return nil, types.ErrEffectiveEpochNotFound
	}

	storedCount := 0
	for _, validation := range msg.Validations {
		if validation == nil || validation.ModelId == "" || validation.ParticipantAddress == "" {
			continue
		}
		if _, err := sdk.AccAddressFromBech32(validation.ParticipantAddress); err != nil {
			k.LogWarn("[SubmitPoCChallengeValidations] Invalid participant address, skipping", types.PoC,
				"validator", msg.Creator, "participant", validation.ParticipantAddress, "error", err)
			continue
		}
		ch, found, err := k.GetPoCChallenge(goCtx, validation.ParticipantAddress)
		if err != nil {
			return nil, err
		}
		if !found || ch.FailureKind != types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_UNSET {
			continue
		}
		if ch.EpochIndex != epochIndex {
			continue
		}
		if startBlockHeight != ch.StartHeight {
			continue
		}
		finish, err := k.ChallengeFinish(goCtx, ch)
		if err != nil {
			return nil, err
		}
		if finish-ch.StartHeight < MinPunishableSegmentBlocks {
			continue
		}

		targetAddr, err := sdk.AccAddressFromBech32(validation.ParticipantAddress)
		if err != nil {
			continue
		}
		validatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
		if err != nil {
			return nil, sdkerrors.Wrap(types.ErrInvalidAddress, err.Error())
		}
		exists, err := k.PoCChallengeValidations.Has(goCtx, collections.Join3(targetAddr, validation.ModelId, validatorAddr))
		if err != nil {
			return nil, sdkerrors.Wrapf(err, "failed to check existing challenge validation for participant %s", validation.ParticipantAddress)
		}
		if exists {
			k.LogWarn("[SubmitPoCChallengeValidations] Validation already exists, skipping duplicate", types.PoC,
				"validator", msg.Creator, "participant", validation.ParticipantAddress, "model_id", validation.ModelId)
			continue
		}
		stored := types.PoCValidationV2{
			ParticipantAddress:          validation.ParticipantAddress,
			ValidatorParticipantAddress: msg.Creator,
			PocStageStartBlockHeight:    startBlockHeight,
			ValidatedWeight:             validation.ValidatedWeight,
			ModelId:                     validation.ModelId,
		}
		if err := k.PoCChallengeValidations.Set(goCtx, collections.Join3(targetAddr, validation.ModelId, validatorAddr), stored); err != nil {
			return nil, sdkerrors.Wrapf(err, "failed to store challenge validation for participant %s", validation.ParticipantAddress)
		}
		storedCount++
	}
	k.LogInfo("[SubmitPoCChallengeValidations] Batch complete", types.PoC,
		"validator", msg.Creator, "start", startBlockHeight, "storedCount", storedCount)
	return &types.MsgSubmitPoCChallengeValidationsResponse{}, nil
}

func (k msgServer) validateChallengeVoteWindow(ctx context.Context, height, startBlockHeight int64, params types.Params) error {
	if params.EpochParams == nil {
		return sdkerrors.Wrap(types.ErrIllegalState, "epoch params not set")
	}
	event, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return err
	}
	if isActive && event != nil && event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED {
		if event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
			return sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC is not in validation")
		}
		if !event.IsInValidationWindow(height, params.EpochParams) {
			return sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC validation window closed")
		}
		return nil
	}
	upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		return sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "failed to get upcoming epoch")
	}
	epochContext := types.NewEpochContext(*upcomingEpoch, *params.EpochParams)
	if !epochContext.IsValidationExchangeWindow(height) {
		return sdkerrors.Wrap(types.ErrPocTooLate, "PoC validation exchange window is closed")
	}
	_ = startBlockHeight
	return nil
}
