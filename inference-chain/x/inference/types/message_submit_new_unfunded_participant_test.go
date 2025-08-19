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
				Creator:      "invalid_address",
				Address:      validAddress,
				Url:          "https://example.com",
				PubKey:       sample.ValidSECP256K1AccountKey(),
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidSECP256K1AccountKey(),
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "invalid address field",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator:      validCreator,
				Address:      "invalid_address",
				Url:          "https://example.com",
				PubKey:       sample.ValidSECP256K1AccountKey(),
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidSECP256K1AccountKey(),
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid all required fields",
			msg: MsgSubmitNewUnfundedParticipant{
				Creator:      validCreator,
				Address:      validAddress,
				Url:          "https://example.com",
				PubKey:       sample.ValidSECP256K1AccountKey(),
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidED25519ValidatorKey(),
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
				Creator:      validCreator,
				Address:      validAddress,
				Url:          "https://example.com",
				PubKey:       invalidKey,
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidSECP256K1AccountKey(),
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
				Address:      validAddress,
				Url:          "https://example.com",
				PubKey:       sample.ValidSECP256K1AccountKey(),
				ValidatorKey: invalidKey,
				WorkerKey:    sample.ValidSECP256K1AccountKey(),
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
