package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPoCChallengeValidations{}

const (
	// MaxPoCChallengeValidationsPerMsg bounds the entries in one challenge vote
	// message. An honest validator submits one entry per message; the
	// theoretical honest maximum is open challenges times snapshot models.
	MaxPoCChallengeValidationsPerMsg = 64
	// MaxPoCChallengeModelIdLength bounds the model id string in a vote entry.
	MaxPoCChallengeModelIdLength = 256
)

func (msg *MsgSubmitPoCChallengeValidations) ValidateBasic() error {
	if _, err := CanonicalAccAddress(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return ValidatePoCChallengeValidationEntries(msg.Validations)
}

// ValidatePoCChallengeValidationEntries checks the stateless shape of challenge
// vote entries. It is shared by ValidateBasic and the message server, because
// a message nested in authz MsgExec reaches the server without baseapp calling
// ValidateBasic for it.
func ValidatePoCChallengeValidationEntries(entries []*PoCValidationEntryV2) error {
	if len(entries) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "validations must not be empty")
	}
	if len(entries) > MaxPoCChallengeValidationsPerMsg {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"validations exceeds maximum count of %d", MaxPoCChallengeValidationsPerMsg)
	}
	type entryKey struct {
		participant string
		model       string
	}
	seen := make(map[entryKey]struct{}, len(entries))
	for i, entry := range entries {
		if entry == nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "validations[%d] is nil", i)
		}
		if entry.ModelId == "" {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "validations[%d]: model_id is required", i)
		}
		if len(entry.ModelId) > MaxPoCChallengeModelIdLength {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"validations[%d]: model_id exceeds maximum length of %d", i, MaxPoCChallengeModelIdLength)
		}
		if _, err := CanonicalAccAddress(entry.ParticipantAddress); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "validations[%d]: invalid participant address (%s)", i, err)
		}
		key := entryKey{participant: entry.ParticipantAddress, model: entry.ModelId}
		if _, dup := seen[key]; dup {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
				"validations[%d]: duplicate entry for participant %s model %s", i, entry.ParticipantAddress, entry.ModelId)
		}
		seen[key] = struct{}{}
	}
	return nil
}
