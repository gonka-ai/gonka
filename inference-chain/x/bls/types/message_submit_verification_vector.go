package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitVerificationVector{}

const maxVerificationDealerValidityEntries = 65536
const maxVerificationDealerComplaints = 65536

func (m *MsgSubmitVerificationVector) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if m.EpochId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "epoch_id must be > 0")
	}
	if len(m.DealerValidity) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "dealer_validity must be non-empty")
	}
	if len(m.DealerValidity) > maxVerificationDealerValidityEntries {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "dealer_validity exceeds maximum allowed count")
	}

	if len(m.DealerComplaints) > maxVerificationDealerComplaints {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "dealer_complaints exceeds maximum allowed count")
	}
	// A verifier can submit at most one complaint per dealer in a single verification vector.
	if len(m.DealerComplaints) > len(m.DealerValidity) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "dealer_complaints count cannot exceed dealer_validity length")
	}
	seenDealers := make(map[uint32]struct{}, len(m.DealerComplaints))
	for i, complaint := range m.DealerComplaints {
		if complaint.DealerIndex >= uint32(len(m.DealerValidity)) {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "dealer_complaints[%d].dealer_index %d out of range for dealer_validity length %d", i, complaint.DealerIndex, len(m.DealerValidity))
		}
		if m.DealerValidity[complaint.DealerIndex] {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "dealer_complaints[%d].dealer_index %d must correspond to dealer_validity=false", i, complaint.DealerIndex)
		}
		if _, exists := seenDealers[complaint.DealerIndex]; exists {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "duplicate dealer_index %d in dealer_complaints", complaint.DealerIndex)
		}
		seenDealers[complaint.DealerIndex] = struct{}{}
	}
	return nil
}
