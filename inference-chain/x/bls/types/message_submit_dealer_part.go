package types

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitDealerPart{}

const (
	commitmentCompressedG2Len             = 96
	MaxDealerPartCommitmentsCount         = 4096
	MaxEncryptedSharesParticipantsCount   = 4096
	MaxEncryptedSharesPerParticipantCount = 16384
	MaxEncryptedShareCiphertextLen        = 1024
	// geth ECIES minimum: uncompressed R (65) + MAC (32) + AES block (16) = 113.
	// The cosmos-sdk fork length gate is rLen+hLen+1 = 98; a 98-byte MAC-valid
	// blob then panics in symDecrypt (make([]byte, len(ct)-BlockSize)). Honest
	// Encrypt of a 32-byte BLS share is 145 bytes (65-byte R + 16-byte IV +
	// 32-byte ciphertext + 32-byte MAC).
	MinEncryptedShareCiphertextLen    = 113
	HonestEncryptedShareCiphertextLen = 145
)

func ValidateEncryptedShareCiphertextLen(share []byte) error {
	if len(share) == 0 {
		return fmt.Errorf("must be non-empty")
	}
	if len(share) < MinEncryptedShareCiphertextLen {
		return fmt.Errorf("is below ECIES minimum (%d bytes)", MinEncryptedShareCiphertextLen)
	}
	if len(share) > MaxEncryptedShareCiphertextLen {
		return fmt.Errorf("exceeds maximum allowed size")
	}
	return nil
}

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
	if len(m.Commitments) > MaxDealerPartCommitmentsCount {
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
	if len(m.EncryptedSharesForParticipants) > MaxEncryptedSharesParticipantsCount {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants exceeds maximum allowed count")
	}
	for i, participantShares := range m.EncryptedSharesForParticipants {
		if len(participantShares.EncryptedShares) == 0 {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants[%d].encrypted_shares must be non-empty", i)
		}
		if len(participantShares.EncryptedShares) > MaxEncryptedSharesPerParticipantCount {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants[%d].encrypted_shares exceeds maximum allowed count", i)
		}
		for j, shareCiphertext := range participantShares.EncryptedShares {
			if err := ValidateEncryptedShareCiphertextLen(shareCiphertext); err != nil {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "encrypted_shares_for_participants[%d].encrypted_shares[%d] %s", i, j, err.Error())
			}
		}
	}
	return nil
}
