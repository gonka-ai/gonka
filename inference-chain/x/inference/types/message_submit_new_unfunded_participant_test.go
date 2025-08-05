package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSubmitNewUnfundedParticipant_ValidateBasic(t *testing.T) {
	validCreator := sample.AccAddress()
	validAddress := sample.AccAddress()

	tests := []struct {
		name string
		msg  MsgSubmitNewUnfundedParticipant
		err  error
	}{
		{
			name: "invalid creator address",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid creator address",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator: validCreator,
			},
		}, {
			name: "invalid address field",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator: validCreator,
				Address: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address field",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator: validCreator,
				Address: validAddress,
			},
		}, {
			name: "valid pub key",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator: validCreator,
				PubKey:  sample.ValidSECP256K1AccountKey(),
			},
		}, {
			name: "valid validator key",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator:      validCreator,
				ValidatorKey: sample.ValidED25519ValidatorKey(),
			},
		}, {
			name: "valid worker key",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator:   validCreator,
				WorkerKey: sample.ValidSECP256K1AccountKey(),
			},
		}, {
			name: "valid all keys",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator:      validCreator,
				Address:      validAddress,
				PubKey:       sample.ValidSECP256K1AccountKey(),
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidSECP256K1AccountKey(),
			},
		},
	}

	// Add test cases for invalid pub keys
	for name, invalidKey := range sample.InvalidSECP256K1AccountKeys() {
		tests = append(tests, struct {
			name string
			msg  MsgSubmitNewUnfundedParticipant
			err  error
		}{
			name: "invalid pub key: " + name,
			msg: MsgSubmitNewUnfundedParticipant{
				Creator: validCreator,
				PubKey:  invalidKey,
			},
			err: sdkerrors.ErrInvalidPubKey,
		})
	}

	// Add test cases for invalid validator keys
	for name, invalidKey := range sample.InvalidED25519ValidatorKeys() {
		tests = append(tests, struct {
			name string
			msg  MsgSubmitNewUnfundedParticipant
			err  error
		}{
			name: "invalid validator key: " + name,
			msg: MsgSubmitNewUnfundedParticipant{
				Creator:      validCreator,
				ValidatorKey: invalidKey,
			},
			err: sdkerrors.ErrInvalidPubKey,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.ValidateBasic()
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
				return
			}
			require.NoError(t, err)
		})
	}
}
