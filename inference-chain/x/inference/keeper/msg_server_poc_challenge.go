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
	if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
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
	if err := k.CheckEpochModelCommit(goCtx, ch.EpochIndex, addr, msg.Entries); err != nil {
		return nil, err
	}
	existingByModel, err := k.loadExistingChallengeCommits(goCtx, addr)
	if err != nil {
		return nil, err
	}
	updates, _, _, err := k.buildPoCV2CommitUpdates(goCtx, height, msg.Entries, existingByModel)
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
			TreeDepth:                update.entry.TreeDepth,
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
	// Repeated here because a message nested in authz MsgExec reaches the
	// server without baseapp calling ValidateBasic for it.
	if err := types.ValidatePoCChallengeValidationEntries(msg.Validations); err != nil {
		return nil, err
	}
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}
	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}
	validatorAddr, err := types.CanonicalAccAddress(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrapf(types.ErrInvalidAddress, "invalid creator address: %v", err)
	}
	validator := validatorAddr.String()
	if k.IsPoCParticipantBlocked(goCtx, validator) {
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, validator)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	height := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight
	snapshotKey, err := k.validateChallengeVoteWindow(goCtx, height, params)
	if err != nil {
		return nil, err
	}
	epochIndex, ok := k.GetEffectiveEpochIndex(goCtx)
	if !ok {
		return nil, types.ErrEffectiveEpochNotFound
	}

	// Only votes from validators with voting power in the segment's snapshot
	// are counted when the segment is decided, so anything else is dropped
	// here instead of occupying state until the decision.
	snapshot, found, err := k.GetPoCValidationSnapshot(goCtx, snapshotKey)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, sdkerrors.Wrapf(types.ErrPoCChallengeSnapshotMissing, "snapshot key %d", snapshotKey)
	}
	creatorPowerByModel := challengeVoterPowerByModel(snapshot, validator)

	storedCount := 0
	skippedNoPower := 0
	for _, validation := range msg.Validations {
		if validation == nil || validation.ParticipantAddress == "" || validation.ModelId == "" {
			continue
		}
		if creatorPowerByModel[validation.ModelId] <= 0 {
			skippedNoPower++
			continue
		}
		if !k.IsValidGovernanceModel(goCtx, validation.ModelId) {
			continue
		}
		target, err := types.CanonicalAccAddress(validation.ParticipantAddress)
		if err != nil {
			k.LogWarn("[SubmitPoCChallengeValidations] Invalid participant address, skipping", types.PoC,
				"validator", validator, "participant", validation.ParticipantAddress, "error", err)
			continue
		}
		participant := target.String()
		ch, found, err := k.GetPoCChallenge(goCtx, participant)
		if err != nil {
			return nil, err
		}
		if !found || ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
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
		if finish-ch.StartHeight < types.EffectiveMinPunishableSegmentBlocks(params.PocChallengeParams) {
			continue
		}

		exists, err := k.PoCChallengeValidations.Has(goCtx, collections.Join3(target, validation.ModelId, validatorAddr))
		if err != nil {
			return nil, sdkerrors.Wrapf(err, "failed to check existing challenge validation for participant %s", participant)
		}
		if exists {
			k.LogWarn("[SubmitPoCChallengeValidations] Validation already exists, skipping duplicate", types.PoC,
				"validator", validator, "participant", participant, "model_id", validation.ModelId)
			continue
		}
		stored := types.PoCValidationV2{
			ParticipantAddress:          participant,
			ValidatorParticipantAddress: validator,
			PocStageStartBlockHeight:    startBlockHeight,
			ValidatedWeight:             validation.ValidatedWeight,
			ModelId:                     validation.ModelId,
		}
		if err := k.PoCChallengeValidations.Set(goCtx, collections.Join3(target, validation.ModelId, validatorAddr), stored); err != nil {
			return nil, sdkerrors.Wrapf(err, "failed to store challenge validation for participant %s", participant)
		}
		storedCount++
	}
	k.LogInfo("[SubmitPoCChallengeValidations] Batch complete", types.PoC,
		"validator", validator, "start", startBlockHeight, "storedCount", storedCount, "skippedNoPower", skippedNoPower)
	return &types.MsgSubmitPoCChallengeValidationsResponse{}, nil
}

// challengeVoterPowerByModel returns the voting power the validator holds per
// model in the snapshot. Snapshot addresses are canonical bech32, the same
// spelling the segment decision keys votes by, so a plain string comparison
// matches the decision exactly.
func challengeVoterPowerByModel(snapshot types.PoCValidationSnapshot, validator string) map[string]int64 {
	result := make(map[string]int64, len(snapshot.ModelVotingPowers))
	for _, mvw := range snapshot.ModelVotingPowers {
		if mvw == nil {
			continue
		}
		for _, vp := range mvw.VotingPowers {
			if vp != nil && vp.Address == validator {
				result[mvw.ModelId] = vp.VotingPower
				break
			}
		}
	}
	return result
}

// validateChallengeVoteWindow checks that the current height is inside an open
// vote window and returns the key of the validation snapshot that the segment
// decision for this window will read: the confirmation event trigger height
// during a confirmation PoC, otherwise the upcoming epoch's PoC start height.
func (k msgServer) validateChallengeVoteWindow(ctx context.Context, height int64, params types.Params) (int64, error) {
	if params.EpochParams == nil {
		return 0, sdkerrors.Wrap(types.ErrIllegalState, "epoch params not set")
	}
	event, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return 0, err
	}
	if isActive && event != nil && event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED {
		if event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
			return 0, sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC is not in validation")
		}
		if !event.IsInValidationWindow(height, params.EpochParams) {
			return 0, sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC validation window closed")
		}
		return event.TriggerHeight, nil
	}
	upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		return 0, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "failed to get upcoming epoch")
	}
	epochContext := types.NewEpochContext(*upcomingEpoch, *params.EpochParams)
	if !epochContext.IsValidationExchangeWindow(height) {
		return 0, sdkerrors.Wrap(types.ErrPocTooLate, "PoC validation exchange window is closed")
	}
	return upcomingEpoch.PocStartBlockHeight, nil
}
