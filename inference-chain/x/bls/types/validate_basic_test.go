package types

import (
	"testing"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkbech32 "github.com/cosmos/cosmos-sdk/types/bech32"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"
)

// setupBech32 configures the bech32 HRP used by sdk.AccAddressFromBech32
func setupBech32() {
	// Use the same prefix seen in other tests in this repo
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
}

func mkAddr(t *testing.T) string {
	setupBech32()
	// 20-byte address (all 1s)
	bz := make([]byte, 20)
	for i := range bz {
		bz[i] = 1
	}
	addr, err := sdkbech32.ConvertAndEncode("gonka", bz)
	require.NoError(t, err)
	return addr
}

func TestMsgUpdateParams_ValidateBasic(t *testing.T) {
	goodAuthority := mkAddr(t)

	t.Run("valid", func(t *testing.T) {
		msg := &MsgUpdateParams{
			Authority: goodAuthority,
			Params:    DefaultParams(),
		}
		require.NoError(t, msg.ValidateBasic())
	})

	t.Run("invalid authority address", func(t *testing.T) {
		msg := &MsgUpdateParams{
			Authority: "not-an-address",
			Params:    DefaultParams(),
		}
		err := msg.ValidateBasic()
		require.Error(t, err)
	})

	t.Run("invalid params", func(t *testing.T) {
		// Make params invalid: TSlotsDegreeOffset >= ITotalSlots
		p := DefaultParams()
		p.TSlotsDegreeOffset = p.ITotalSlots
		msg := &MsgUpdateParams{Authority: goodAuthority, Params: p}
		err := msg.ValidateBasic()
		require.Error(t, err)
	})
}

func TestMsgSubmitDealerPart_ValidateBasic(t *testing.T) {
	creator := mkAddr(t)

	validCommitment := make([]byte, commitmentCompressedG2Len)
	validCommitment[0] = 0x01
	validShare := []byte{0x01, 0x02}

	t.Run("valid", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{
			Creator:     creator,
			EpochId:     1,
			Commitments: [][]byte{validCommitment},
			EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{
				EncryptedShares: [][]byte{validShare},
			}},
		}
		require.NoError(t, msg.ValidateBasic())
	})

	t.Run("invalid creator", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{Creator: "bad", EpochId: 1, Commitments: [][]byte{validCommitment}, EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{EncryptedShares: [][]byte{validShare}}}}
		err := msg.ValidateBasic()
		require.Error(t, err)
		require.True(t, errorsmod.IsOf(err, sdkerrors.ErrInvalidAddress))
	})

	t.Run("epoch zero", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{Creator: creator, EpochId: 0, Commitments: [][]byte{validCommitment}, EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{EncryptedShares: [][]byte{validShare}}}}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty commitments", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{Creator: creator, EpochId: 1, Commitments: nil, EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{EncryptedShares: [][]byte{validShare}}}}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty encrypted shares list", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{Creator: creator, EpochId: 1, Commitments: [][]byte{validCommitment}, EncryptedSharesForParticipants: nil}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid commitment length", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{
			Creator:     creator,
			EpochId:     1,
			Commitments: [][]byte{make([]byte, commitmentCompressedG2Len-1)},
			EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{
				EncryptedShares: [][]byte{validShare},
			}},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("all-zero commitment", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{
			Creator:     creator,
			EpochId:     1,
			Commitments: [][]byte{make([]byte, commitmentCompressedG2Len)},
			EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{
				EncryptedShares: [][]byte{validShare},
			}},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty encrypted shares for participant", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{
			Creator:     creator,
			EpochId:     1,
			Commitments: [][]byte{validCommitment},
			EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{
				EncryptedShares: nil,
			}},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty encrypted share ciphertext", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{
			Creator:     creator,
			EpochId:     1,
			Commitments: [][]byte{validCommitment},
			EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{
				EncryptedShares: [][]byte{{}},
			}},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("oversized encrypted share ciphertext", func(t *testing.T) {
		msg := &MsgSubmitDealerPart{
			Creator:     creator,
			EpochId:     1,
			Commitments: [][]byte{validCommitment},
			EncryptedSharesForParticipants: []EncryptedSharesForParticipant{{
				EncryptedShares: [][]byte{make([]byte, maxEncryptedShareCiphertextLen+1)},
			}},
		}
		require.Error(t, msg.ValidateBasic())
	})
}

func TestMsgSubmitVerificationVector_ValidateBasic(t *testing.T) {
	creator := mkAddr(t)

	t.Run("valid", func(t *testing.T) {
		msg := &MsgSubmitVerificationVector{Creator: creator, EpochId: 1, DealerValidity: []bool{true, false}}
		require.NoError(t, msg.ValidateBasic())
	})

	t.Run("invalid creator", func(t *testing.T) {
		msg := &MsgSubmitVerificationVector{Creator: "bad", EpochId: 1, DealerValidity: []bool{true}}
		err := msg.ValidateBasic()
		require.Error(t, err)
		require.True(t, errorsmod.IsOf(err, sdkerrors.ErrInvalidAddress))
	})

	t.Run("epoch zero", func(t *testing.T) {
		msg := &MsgSubmitVerificationVector{Creator: creator, EpochId: 0, DealerValidity: []bool{true}}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty dealer_validity", func(t *testing.T) {
		msg := &MsgSubmitVerificationVector{Creator: creator, EpochId: 1, DealerValidity: nil}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("too many dealer_validity entries", func(t *testing.T) {
		tooMany := make([]bool, maxVerificationDealerValidityEntries+1)
		msg := &MsgSubmitVerificationVector{Creator: creator, EpochId: 1, DealerValidity: tooMany}
		require.Error(t, msg.ValidateBasic())
	})
}

func TestMsgSubmitGroupKeyValidationSignature_ValidateBasic(t *testing.T) {
	creator := mkAddr(t)
	validPartialSignature := make([]byte, groupValidationSignatureChunkLen*2)
	validPartialSignature[0] = 1
	validPartialSignature[groupValidationSignatureChunkLen] = 1

	t.Run("valid", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       1,
			SlotIndices:      []uint32{0, 2},
			PartialSignature: validPartialSignature,
		}
		require.NoError(t, msg.ValidateBasic())
	})

	t.Run("invalid creator", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          "bad",
			NewEpochId:       1,
			SlotIndices:      []uint32{0},
			PartialSignature: []byte{1, 2, 3},
		}
		err := msg.ValidateBasic()
		require.Error(t, err)
		require.True(t, errorsmod.IsOf(err, sdkerrors.ErrInvalidAddress))
	})

	t.Run("epoch zero", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       0,
			SlotIndices:      []uint32{0},
			PartialSignature: []byte{1, 2, 3},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty slot indices", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       1,
			SlotIndices:      nil,
			PartialSignature: []byte{1, 2, 3},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("duplicate slot indices", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       1,
			SlotIndices:      []uint32{0, 0},
			PartialSignature: validPartialSignature,
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty partial signature", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       1,
			SlotIndices:      []uint32{0},
			PartialSignature: nil,
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("partial signature length not multiple of 48", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       1,
			SlotIndices:      []uint32{0},
			PartialSignature: make([]byte, groupValidationSignatureChunkLen-1),
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("partial signature count mismatch", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       1,
			SlotIndices:      []uint32{0, 1},
			PartialSignature: make([]byte, groupValidationSignatureChunkLen),
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("all-zero signature chunk", func(t *testing.T) {
		msg := &MsgSubmitGroupKeyValidationSignature{
			Creator:          creator,
			NewEpochId:       1,
			SlotIndices:      []uint32{0},
			PartialSignature: make([]byte, groupValidationSignatureChunkLen),
		}
		require.Error(t, msg.ValidateBasic())
	})
}

func TestMsgSubmitPartialSignature_ValidateBasic(t *testing.T) {
	creator := mkAddr(t)
	validPartialSignature := make([]byte, partialSignatureChunkLen*2)
	validPartialSignature[0] = 1
	validPartialSignature[partialSignatureChunkLen] = 1

	t.Run("valid", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      []uint32{1, 2},
			PartialSignature: validPartialSignature,
		}
		require.NoError(t, msg.ValidateBasic())
	})

	t.Run("invalid creator", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          "bad",
			SlotIndices:      []uint32{1},
			PartialSignature: []byte{1},
		}
		err := msg.ValidateBasic()
		require.Error(t, err)
		require.True(t, errorsmod.IsOf(err, sdkerrors.ErrInvalidAddress))
	})

	t.Run("empty slot indices", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      nil,
			PartialSignature: []byte{1},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("duplicate slot indices", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      []uint32{1, 2, 1},
			PartialSignature: []byte{1, 2, 3},
		}
		err := msg.ValidateBasic()
		require.Error(t, err)
		require.True(t, errorsmod.IsOf(err, sdkerrors.ErrInvalidRequest))
		require.Contains(t, err.Error(), "contains duplicates")
	})

	t.Run("empty partial signature", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      []uint32{1},
			PartialSignature: nil,
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("partial signature length not multiple of 48", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      []uint32{1},
			PartialSignature: make([]byte, partialSignatureChunkLen-1),
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("partial signature count mismatch", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      []uint32{1, 2},
			PartialSignature: make([]byte, partialSignatureChunkLen),
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("all-zero signature chunk", func(t *testing.T) {
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      []uint32{1},
			PartialSignature: make([]byte, partialSignatureChunkLen),
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("too many slot indices", func(t *testing.T) {
		tooManySlots := make([]uint32, maxPartialSignatureSlotIndices+1)
		tooManySignature := make([]byte, len(tooManySlots)*partialSignatureChunkLen)
		tooManySignature[0] = 1
		msg := &MsgSubmitPartialSignature{
			Creator:          creator,
			SlotIndices:      tooManySlots,
			PartialSignature: tooManySignature,
		}
		require.Error(t, msg.ValidateBasic())
	})
}

func TestMsgRequestThresholdSignature_ValidateBasic(t *testing.T) {
	creator := mkAddr(t)
	chainID := make([]byte, 32)
	requestID := make([]byte, 32)
	data := [][]byte{make([]byte, 32)}

	t.Run("valid", func(t *testing.T) {
		msg := &MsgRequestThresholdSignature{
			Creator:        creator,
			CurrentEpochId: 1,
			ChainId:        chainID,
			RequestId:      requestID,
			Data:           data,
		}
		require.NoError(t, msg.ValidateBasic())
	})

	t.Run("invalid creator", func(t *testing.T) {
		msg := &MsgRequestThresholdSignature{Creator: "bad", CurrentEpochId: 1, ChainId: chainID, RequestId: requestID, Data: data}
		err := msg.ValidateBasic()
		require.Error(t, err)
		require.True(t, errorsmod.IsOf(err, sdkerrors.ErrInvalidAddress))
	})

	t.Run("epoch zero", func(t *testing.T) {
		msg := &MsgRequestThresholdSignature{Creator: creator, CurrentEpochId: 0, ChainId: chainID, RequestId: requestID, Data: data}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("empty data", func(t *testing.T) {
		msg := &MsgRequestThresholdSignature{Creator: creator, CurrentEpochId: 1, ChainId: chainID, RequestId: requestID, Data: nil}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid chain_id length", func(t *testing.T) {
		msg := &MsgRequestThresholdSignature{Creator: creator, CurrentEpochId: 1, ChainId: make([]byte, 31), RequestId: requestID, Data: data}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid request_id length", func(t *testing.T) {
		msg := &MsgRequestThresholdSignature{Creator: creator, CurrentEpochId: 1, ChainId: chainID, RequestId: make([]byte, 33), Data: data}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid data chunk length", func(t *testing.T) {
		msg := &MsgRequestThresholdSignature{
			Creator:        creator,
			CurrentEpochId: 1,
			ChainId:        chainID,
			RequestId:      requestID,
			Data:           [][]byte{make([]byte, 31)},
		}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("too many data chunks", func(t *testing.T) {
		tooMany := make([][]byte, maxThresholdSigningDataChunks+1)
		for i := range tooMany {
			tooMany[i] = make([]byte, 32)
		}
		msg := &MsgRequestThresholdSignature{
			Creator:        creator,
			CurrentEpochId: 1,
			ChainId:        chainID,
			RequestId:      requestID,
			Data:           tooMany,
		}
		require.Error(t, msg.ValidateBasic())
	})
}
