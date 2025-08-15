package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSubmitNewParticipant_ValidateBasic(t *testing.T) {
	validCreator := sample.AccAddress()

	tests := []struct {
		name string
		msg  MsgSubmitNewParticipant
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgSubmitNewParticipant{
				Creator:      "invalid_address",
				Url:          "https://example.com",
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidSECP256K1AccountKey(),
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid participant",
			msg: MsgSubmitNewParticipant{
				Creator:      validCreator,
				Url:          "https://example.com",
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidED25519ValidatorKey(),
			},
		},
	}

	// Add test cases for invalid validator keys
	for name, invalidKey := range sample.InvalidED25519ValidatorKeys() {
		tests = append(tests, struct {
			name string
			msg  MsgSubmitNewParticipant
			err  error
		}{
			name: "invalid validator key: " + name,
			msg: MsgSubmitNewParticipant{
				Creator:      validCreator,
				Url:          "https://example.com",
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
