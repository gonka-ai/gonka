package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPartialSignature{}

const (
	partialSignatureChunkLen      = 48
	maxPartialSignatureSlotIndices = 65536
)

func (m *MsgSubmitPartialSignature) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if len(m.SlotIndices) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "slot_indices must be non-empty")
	}
	if len(m.SlotIndices) > maxPartialSignatureSlotIndices {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "slot_indices exceeds maximum allowed count")
	}
	seen := make(map[uint32]struct{}, len(m.SlotIndices))
	for _, slot := range m.SlotIndices {
		if _, exists := seen[slot]; exists {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "slot_indices contains duplicates")
		}
		seen[slot] = struct{}{}
	}
	if len(m.PartialSignature) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "partial_signature must be non-empty")
	}
	if len(m.PartialSignature)%partialSignatureChunkLen != 0 {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "partial_signature length must be a multiple of %d bytes", partialSignatureChunkLen)
	}
	if len(m.PartialSignature)/partialSignatureChunkLen != len(m.SlotIndices) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "partial_signature count must match slot_indices count")
	}
	for i := 0; i < len(m.PartialSignature); i += partialSignatureChunkLen {
		chunk := m.PartialSignature[i : i+partialSignatureChunkLen]
		allZero := true
		for _, b := range chunk {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "partial_signature contains all-zero chunk")
		}
	}
	return nil
}
