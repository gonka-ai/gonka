package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SubmitPocValidationsV2 handles batch submission of PoC v2 validations.
func (k msgServer) SubmitPocValidationsV2(goCtx context.Context, msg *types.MsgSubmitPocValidationsV2) (*types.MsgSubmitPocValidationsV2Response, error) {
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}
	if len(msg.Validations) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "validations must not be empty")
	}
	if len(msg.Validations) > params.GetEpochValidationParams().ValidationsPerMessageLimit() {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "too many validations in one message")
	}
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Check for active confirmation PoC event
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	creatorAddr, err := decodePoCCreator(msg.Creator)
	if err != nil {
		return nil, err
	}
	if k.IsPoCParticipantBlocked(ctx, creatorAddr.String()) {
		k.LogError(PocFailureTag+"[SubmitPocValidationsV2] validator is blocked from PoC", types.PoC, "validator", creatorAddr.String())
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, creatorAddr.String())
	}
	if _, err := types.CanonicalAccAddress(msg.Creator); err != nil {
		return nil, err
	}

	// Validate PoC window once at message level (all validations share the same height)
	if isActive && activeEvent != nil && activeEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		// Verify the message is for this confirmation PoC event
		if startBlockHeight != activeEvent.TriggerHeight {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Confirmation PoC: start block height mismatch", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"event.TriggerHeight", activeEvent.TriggerHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocValidationsV2] Confirmation PoC active but start block height doesn't match. "+
				"validatorParticipant = %s. msg.PocStageStartBlockHeight = %d. event.TriggerHeight = %d",
				msg.Creator, startBlockHeight, activeEvent.TriggerHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		// Verify we're in the validation window
		confirmParams, err := k.GetParams(ctx)
		if err != nil {
			return nil, err
		}
		epochParams := confirmParams.EpochParams
		if !activeEvent.IsInValidationWindow(currentBlockHeight, epochParams) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Confirmation PoC: outside validation window", types.PoC,
				"validatorParticipant", msg.Creator,
				"currentBlockHeight", currentBlockHeight,
				"validationStartHeight", activeEvent.GetValidationStart(epochParams),
				"validationEndHeight", activeEvent.GetValidationEnd(epochParams))
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "Confirmation PoC validation window closed")
		}
	} else {
		// Regular PoC logic
		regularParams, err := k.Keeper.GetParams(ctx)
		if err != nil {
			return nil, err
		}
		epochParams := regularParams.EpochParams
		upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
		if !found {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Failed to get upcoming epoch", types.PoC,
				"validatorParticipant", msg.Creator,
				"currentBlockHeight", currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "[SubmitPocValidationsV2] Failed to get upcoming epoch")
		}
		epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

		if !epochContext.IsStartOfPocStage(startBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] message start block height doesn't match the upcoming epoch", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocValidationsV2] message start block height doesn't match the upcoming epoch. "+
				"validatorParticipant = %s. msg.PocStageStartBlockHeight = %d. epochContext.PocStartBlockHeight = %d. currentBlockHeight = %d",
				msg.Creator, startBlockHeight, epochContext.PocStartBlockHeight, currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		if !epochContext.IsValidationExchangeWindow(currentBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] PoC validation exchange window is closed.", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.BlockHeight", startBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
		}
	}

	validator := creatorAddr.String()
	limit := params.GetEpochValidationParams().ValidationsPerSignerPerStageLimit()
	storedForSigner, err := k.CountPocValidationsV2ForValidator(ctx, startBlockHeight, creatorAddr)
	if err != nil {
		return nil, err
	}
	// batchHasPositive: does this batch carry any consensus-bearing (positive)
	// vote? A positive vote may reclaim one of the signer's own non-positive rows
	// when the signer is at its stage cap, so a batch that has one is not dead on
	// arrival even at the cap.
	batchHasPositive := false
	for _, v := range msg.Validations {
		if v != nil && v.ValidatedWeight > 0 {
			batchHasPositive = true
			break
		}
	}
	if storedForSigner >= limit {
		// At the cap the batch is only worth processing if it can store a positive
		// vote by evicting one of the signer's non-positive rows. Otherwise reject,
		// preserving the old contract (a batch that cannot store anything errors).
		canEvict := false
		if batchHasPositive {
			canEvict, err = k.HasNonPositiveValidationV2ForSigner(ctx, startBlockHeight, creatorAddr)
			if err != nil {
				return nil, err
			}
		}
		if !canEvict {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "signer stage cap reached")
		}
	}

	// Process each validation - skip failures, don't fail entire batch.
	// A proposer can include a transaction CheckTx would have dropped. Skipping
	// a bad row is what stops that row from being stored.
	storedCount := 0
	for _, validation := range msg.Validations {
		if validation == nil {
			continue
		}
		modelID := validation.ModelId
		if modelID == "" || !k.IsValidGovernanceModel(ctx, modelID) {
			k.LogWarn("[SubmitPocValidationsV2] model_id is not a governance model, skipping", types.PoC,
				"validator", msg.Creator,
				"participant", validation.ParticipantAddress)
			continue
		}

		targetAddr, err := types.CanonicalAccAddress(validation.ParticipantAddress)
		if err != nil {
			k.LogWarn("[SubmitPocValidationsV2] Invalid participant address, skipping", types.PoC,
				"validator", msg.Creator,
				"participant", validation.ParticipantAddress,
				"error", err)
			continue
		}
		participant := targetAddr.String()
		if _, found := k.GetParticipant(ctx, participant); !found {
			k.LogWarn("[SubmitPocValidationsV2] target is not a participant, skipping", types.PoC,
				"validator", msg.Creator,
				"participant", validation.ParticipantAddress)
			continue
		}
		hasCommit, err := k.HasPoCV2StoreCommit(ctx, startBlockHeight, participant, modelID)
		if err != nil {
			return nil, err
		}
		if !hasCommit {
			k.LogWarn("[SubmitPocValidationsV2] no store commit for target and model, skipping", types.PoC,
				"validator", msg.Creator,
				"participant", participant,
				"model_id", modelID)
			continue
		}

		// Check for duplicate submission (prevents vote flipping)
		exists, err := k.HasPocValidationV2(ctx, startBlockHeight, participant, modelID, validator)
		if err != nil {
			k.LogError("[SubmitPocValidationsV2] Unexpected storage read error, aborting batch", types.PoC,
				"validator", validator,
				"participant", participant,
				"error", err)
			return nil, sdkerrors.Wrapf(err, "[SubmitPocValidationsV2] failed to check existing validation for participant %s", participant)
		}
		if exists {
			k.LogWarn("[SubmitPocValidationsV2] Validation already exists, skipping duplicate", types.PoC,
				"validator", validator,
				"participant", participant,
				"model_id", modelID,
				"stage", startBlockHeight)
			continue
		}

		// At the cap a -1 is skipped and a +1 reclaims one of the signer's own -1 rows.
		if storedForSigner >= limit {
			if validation.ValidatedWeight <= 0 {
				continue
			}
			evicted, err := k.EvictOneNonPositiveValidationV2ForSigner(ctx, startBlockHeight, creatorAddr)
			if err != nil {
				return nil, err
			}
			if !evicted {
				k.LogWarn("[SubmitPocValidationsV2] signer stage cap reached, no evictable row, skipping", types.PoC,
					"validator", validator,
					"stage", startBlockHeight,
					"stored", storedForSigner)
				continue
			}
			storedForSigner--
		}

		// Store the v2 validation (combine message-level height with payload)
		storedValidation := types.PoCValidationV2{
			ParticipantAddress:          participant,
			ValidatorParticipantAddress: validator,
			PocStageStartBlockHeight:    startBlockHeight,
			ModelId:                     modelID,
			ValidatedWeight:             validation.ValidatedWeight,
		}

		if err := k.SetPocValidationV2(ctx, storedValidation); err != nil {
			k.LogError("[SubmitPocValidationsV2] Unexpected storage write error, aborting batch", types.PoC,
				"validator", msg.Creator,
				"participant", validation.ParticipantAddress,
				"model_id", modelID,
				"error", err)
			return nil, sdkerrors.Wrapf(err, "[SubmitPocValidationsV2] failed to store validation for participant %s", validation.ParticipantAddress)
		}

		storedCount++
		storedForSigner++
		k.LogInfo("[SubmitPocValidationsV2] Validation stored", types.PoC,
			"validator", validator,
			"participant", participant,
			"model_id", modelID,
			"validatedWeight", validation.ValidatedWeight)
	}

	k.LogInfo("[SubmitPocValidationsV2] Batch complete", types.PoC,
		"validator", msg.Creator,
		"totalInBatch", len(msg.Validations),
		"storedCount", storedCount)

	return &types.MsgSubmitPocValidationsV2Response{}, nil
}
