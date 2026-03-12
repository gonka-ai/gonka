package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitDealerPart{}

const (
	commitmentCompressedG2Len             = 96
	maxDealerPartCommitmentsCount         = 4096
	maxEncryptedSharesParticipantsCount   = 4096
	maxEncryptedSharesPerParticipantCount = 16384
	maxEncryptedShareCiphertextLen        = 1024
)

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
	if len(m.Commitments) > maxDealerPartCommitmentsCount {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "commitments exceeds maximum allowed count")
	}
	for i, commitment := range m.Commitments {
		if len(commitment) != commitmentCompressedG2Len {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "commitments[%d] must be exactly %d bytes", i, commitmentCompressedG2Len)
		}
		allZero := true
		for _, b := range commitment {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "commitments[%d] must not be all-zero bytes", i)
		}
	}
	// encrypted shares for participants: non-empty, bounded, and each entry non-empty with non-empty shares
	if len(m.EncryptedSharesForParticipants) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants must be non-empty")
	}
	if len(m.EncryptedSharesForParticipants) > maxEncryptedSharesParticipantsCount {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants exceeds maximum allowed count")
	}
	for i, participantShares := range m.EncryptedSharesForParticipants {
		if len(participantShares.EncryptedShares) == 0 {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants[%d].encrypted_shares must be non-empty", i)
		}
		if len(participantShares.EncryptedShares) > maxEncryptedSharesPerParticipantCount {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants[%d].encrypted_shares exceeds maximum allowed count", i)
		}
		for j, shareCiphertext := range participantShares.EncryptedShares {
			if len(shareCiphertext) == 0 {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants[%d].encrypted_shares[%d] must be non-empty", i, j)
			}
			if len(shareCiphertext) > maxEncryptedShareCiphertextLen {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants[%d].encrypted_shares[%d] exceeds maximum allowed size", i, j)
			}
		}
	}
	return nil
}
