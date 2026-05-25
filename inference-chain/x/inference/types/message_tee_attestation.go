package types

import (
	"crypto/sha256"
	"fmt"
	"unicode"
	"unicode/utf8"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var (
	_ sdk.Msg = &MsgSubmitTeeAttestation{}
	_ sdk.Msg = &MsgRevokeTeeAttestation{}
	_ sdk.Msg = &MsgSubmitTeeValidations{}
)

const (
	MaxTeeNodeIDLength = 128

	MaxTeeAttestationEntriesHard = 4096
	MaxTeeRevokeEntriesHard      = MaxTeeAttestationEntriesHard
	MaxTeeValidationEntriesHard  = MaxTeeAttestationEntriesHard
	MaxTeeQuoteBytes             = 64 * 1024
	MaxTeeValidationReasonBytes  = 512
)

func (msg *MsgSubmitTeeAttestation) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if msg.PocStageStartBlockHeight <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "poc_stage_start_block_height must be > 0")
	}
	if len(msg.Attestations) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "attestations must be non-empty")
	}
	if len(msg.Attestations) > MaxTeeAttestationEntriesHard {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"attestations exceeds hard cap of %d", MaxTeeAttestationEntriesHard)
	}

	seenNodeIDs := make(map[string]struct{}, len(msg.Attestations))
	for i, entry := range msg.Attestations {
		if entry == nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "attestations[%d] is nil", i)
		}
		if entry.NodeId == "" {
			return errorsmod.Wrapf(ErrTeeAttestationNodeIdEmpty, "attestations[%d]", i)
		}
		if len(entry.NodeId) > MaxTeeNodeIDLength {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"attestations[%d].node_id exceeds %d bytes", i, MaxTeeNodeIDLength)
		}
		if _, dup := seenNodeIDs[entry.NodeId]; dup {
			return errorsmod.Wrapf(ErrDuplicateNodeId,
				"attestations[%d].node_id %q appears more than once", i, entry.NodeId)
		}
		seenNodeIDs[entry.NodeId] = struct{}{}

		if len(entry.PublicKey) == 0 {
			return errorsmod.Wrapf(ErrTeeAttestationInvalidPublicKey,
				"attestations[%d].public_key must be non-empty", i)
		}

		if len(entry.QuoteHash) > 0 && len(entry.QuoteHash) != sha256.Size {
			return errorsmod.Wrap(ErrTeeAttestationQuoteRequired,
				fmt.Sprintf("attestations[%d].quote_hash must be %d bytes sha256, got %d",
					i, sha256.Size, len(entry.QuoteHash)))
		}

		if len(entry.Quote) > MaxTeeQuoteBytes {
			return errorsmod.Wrapf(ErrTeeAttestationQuoteTooLarge,
				"attestations[%d].quote is %d bytes, max %d", i, len(entry.Quote), MaxTeeQuoteBytes)
		}

		if entry.Measurement != "" {
			if _, err := CanonicalMeasurement(entry.Measurement); err != nil {
				return errorsmod.Wrapf(ErrTeeAttestationMeasurementNotAllowed,
					"attestations[%d]: %v", i, err)
			}
		}
	}
	return nil
}

func (msg *MsgSubmitTeeValidations) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if msg.PocStageStartBlockHeight <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "poc_stage_start_block_height must be > 0")
	}
	if len(msg.Validations) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "validations must be non-empty")
	}
	if len(msg.Validations) > MaxTeeValidationEntriesHard {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"validations exceeds hard cap of %d", MaxTeeValidationEntriesHard)
	}

	type key struct{ subject, node string }
	seen := make(map[key]struct{}, len(msg.Validations))
	for i, v := range msg.Validations {
		if v == nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "validations[%d] is nil", i)
		}
		if _, err := sdk.AccAddressFromBech32(v.SubjectAddress); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress,
				"validations[%d].subject_address: %v", i, err)
		}
		if v.NodeId == "" {
			return errorsmod.Wrapf(ErrTeeAttestationNodeIdEmpty, "validations[%d]", i)
		}
		if len(v.NodeId) > MaxTeeNodeIDLength {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"validations[%d].node_id exceeds %d bytes", i, MaxTeeNodeIDLength)
		}
		k := key{v.SubjectAddress, v.NodeId}
		if _, dup := seen[k]; dup {
			return errorsmod.Wrapf(ErrTeeValidationAlreadyExists,
				"validations[%d] (%s, %s) duplicated within message", i, v.SubjectAddress, v.NodeId)
		}
		seen[k] = struct{}{}

		switch v.Verdict {
		case TeeVerdict_TEE_VERDICT_OK, TeeVerdict_TEE_VERDICT_REJECT:
		default:
			return errorsmod.Wrapf(ErrTeeValidationVerdictUnspecified, "validations[%d]", i)
		}
		if v.Verdict == TeeVerdict_TEE_VERDICT_OK && v.MeasurementSeen == "" {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"validations[%d].measurement_seen is required for OK verdict", i)
		}

		if v.MeasurementSeen != "" {
			if _, err := CanonicalMeasurement(v.MeasurementSeen); err != nil {
				return errorsmod.Wrapf(ErrTeeAttestationMeasurementNotAllowed,
					"validations[%d].measurement_seen: %v", i, err)
			}
		}
		if len(v.Reason) > MaxTeeValidationReasonBytes {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"validations[%d].reason exceeds %d bytes", i, MaxTeeValidationReasonBytes)
		}
		if !utf8.ValidString(v.Reason) {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"validations[%d].reason must be valid UTF-8", i)
		}
		for _, r := range v.Reason {
			if !unicode.IsPrint(r) {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
					"validations[%d].reason contains non-printable characters", i)
			}
		}
	}
	return nil
}

func (msg *MsgRevokeTeeAttestation) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if len(msg.NodeIds) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "node_ids must be non-empty")
	}
	if len(msg.NodeIds) > MaxTeeRevokeEntriesHard {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"node_ids exceeds hard cap of %d", MaxTeeRevokeEntriesHard)
	}
	seen := make(map[string]struct{}, len(msg.NodeIds))
	for i, id := range msg.NodeIds {
		if id == "" {
			return errorsmod.Wrapf(ErrTeeAttestationNodeIdEmpty, "node_ids[%d]", i)
		}
		if len(id) > MaxTeeNodeIDLength {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"node_ids[%d] exceeds %d bytes", i, MaxTeeNodeIDLength)
		}
		if _, dup := seen[id]; dup {
			return errorsmod.Wrapf(ErrDuplicateNodeId,
				"node_ids[%d] %q appears more than once", i, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
