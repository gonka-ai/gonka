package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitDealerPart{}

func (m *MsgSubmitDealerPart) ValidateBasic() error {
	// creator address
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	// epoch id
	if m.EpochId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "epoch_id must be > 0")
	}
	// commitments: non-empty, each G2 size and non-zero bytes
	if len(m.Commitments) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "commitments must be non-empty")
	}
	// encrypted shares for participants: non-empty, bounded, and each entry non-empty with non-empty shares
	if len(m.EncryptedSharesForParticipants) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants must be non-empty")
	}
	return nil
}
