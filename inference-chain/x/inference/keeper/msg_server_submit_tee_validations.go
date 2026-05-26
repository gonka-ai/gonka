package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitTeeValidations(goCtx context.Context, msg *types.MsgSubmitTeeValidations) (*types.MsgSubmitTeeValidationsResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}
	maxEntries := int(types.DefaultMaxTeeValidationEntriesPerMessage)
	if params.TeeAttestationParams != nil && params.TeeAttestationParams.MaxValidationEntriesPerMessage > 0 {
		maxEntries = int(params.TeeAttestationParams.MaxValidationEntriesPerMessage)
	}
	if len(msg.Validations) > maxEntries {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("too many validation entries: got %d, max %d", len(msg.Validations), maxEntries))
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "TEE validation requires PoC V2 enabled")
	}

	if k.IsPoCParticipantBlocked(goCtx, msg.Creator) {
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentHeight := ctx.BlockHeight()
	startHeight := msg.PocStageStartBlockHeight

	if err := k.assertInPoCValidationWindow(ctx, msg.Creator, startHeight, currentHeight); err != nil {
		return nil, err
	}

	validatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, err.Error())
	}

	stored, skipped := 0, 0
	for i, entry := range msg.Validations {
		if entry == nil {
			skipped++
			continue
		}
		subjectAddr, err := sdk.AccAddressFromBech32(entry.SubjectAddress)
		if err != nil {
			k.LogWarn("[SubmitTeeValidations] invalid subject_address, skipping", types.PoC,
				"validator", msg.Creator, "index", i, "subject", entry.SubjectAddress, "err", err)
			skipped++
			continue
		}
		if subjectAddr.Equals(validatorAddr) {
			k.LogWarn("[SubmitTeeValidations] self-vote rejected, skipping", types.PoC,
				"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId)
			skipped++
			continue
		}

		att, found, err := k.GetTeeAttestation(ctx, subjectAddr, entry.NodeId)
		if err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("attestation lookup failed for subject=%s node_id=%s: %v", entry.SubjectAddress, entry.NodeId, err))
		}
		if !found {
			k.LogWarn("[SubmitTeeValidations] no matching attestation, skipping", types.PoC,
				"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId)
			skipped++
			continue
		}
		if att.AttestedAtHeight != startHeight {
			k.LogWarn("[SubmitTeeValidations] attestation stage mismatch, skipping", types.PoC,
				"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId,
				"entry_stage", startHeight, "attestation_stage", att.AttestedAtHeight)
			skipped++
			continue
		}
		if att.ExpiresAtHeight <= currentHeight {
			k.LogWarn("[SubmitTeeValidations] attestation expired, skipping", types.PoC,
				"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId,
				"expires_at_height", att.ExpiresAtHeight, "current_height", currentHeight)
			skipped++
			continue
		}
		if att.VerificationStatus != types.TeeVerificationStatus_TEE_VERIFICATION_PENDING {
			k.LogWarn("[SubmitTeeValidations] attestation is not pending, skipping", types.PoC,
				"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId,
				"status", att.VerificationStatus.String())
			skipped++
			continue
		}

		exists, err := k.HasTeeValidation(ctx, startHeight, subjectAddr, entry.NodeId, validatorAddr)
		if err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("dedup check failed for subject=%s node_id=%s: %v", entry.SubjectAddress, entry.NodeId, err))
		}
		if exists {
			skipped++
			continue
		}

		verdict := entry.Verdict
		reason := entry.Reason
		measurementSeen := entry.MeasurementSeen
		if verdict == types.TeeVerdict_TEE_VERDICT_OK {
			if measurementSeen == "" {
				k.LogWarn("[SubmitTeeValidations] invalid OK vote (missing measurement_seen), skipping", types.PoC,
					"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId)
				skipped++
				continue
			} else {
				seen, mErr := types.CanonicalMeasurement(measurementSeen)
				switch {
				case mErr != nil:
					k.LogWarn("[SubmitTeeValidations] invalid OK vote (non-canonical measurement_seen), skipping", types.PoC,
						"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId,
						"raw", measurementSeen, "err", mErr)
					skipped++
					continue
				case seen != att.Measurement:
					k.LogWarn("[SubmitTeeValidations] invalid OK vote (measurement mismatch), skipping", types.PoC,
						"validator", msg.Creator, "subject", entry.SubjectAddress, "node_id", entry.NodeId,
						"extracted", seen, "claimed", att.Measurement)
					skipped++
					continue
				default:
					measurementSeen = seen
				}
			}
		}

		v := types.TeeValidation{
			PocStageStartBlockHeight: startHeight,
			SubjectAddress:           entry.SubjectAddress,
			NodeId:                   entry.NodeId,
			ValidatorAddress:         msg.Creator,
			MeasurementSeen:          measurementSeen,
			Verdict:                  verdict,
			Reason:                   reason,
			SubmittedAtHeight:        currentHeight,
		}
		if err := k.SetTeeValidation(ctx, v); err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("failed to store vote for subject=%s node_id=%s: %v", entry.SubjectAddress, entry.NodeId, err))
		}
		stored++
	}

	k.LogInfo("[SubmitTeeValidations] batch complete", types.PoC,
		"validator", msg.Creator,
		"stage", startHeight,
		"total", len(msg.Validations),
		"stored", stored,
		"skipped", skipped)

	return &types.MsgSubmitTeeValidationsResponse{}, nil
}

func (k Keeper) assertInPoCValidationWindow(ctx sdk.Context, validator string, startHeight, currentHeight int64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	upcoming, found := k.GetUpcomingEpoch(ctx)
	if !found {
		return sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "no upcoming epoch for TEE validation")
	}
	epochCtx := types.NewEpochContext(*upcoming, *params.EpochParams)
	if !epochCtx.IsStartOfPocStage(startHeight) {
		return sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
			fmt.Sprintf("validator=%s msg.startHeight=%d expected=%d",
				validator, startHeight, epochCtx.PocStartBlockHeight))
	}
	if !epochCtx.IsValidationExchangeWindow(currentHeight) {
		return sdkerrors.Wrap(types.ErrTeeValidationOutsideWindow,
			fmt.Sprintf("validator=%s startHeight=%d currentHeight=%d", validator, startHeight, currentHeight))
	}
	return nil
}
