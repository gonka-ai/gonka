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
				Creator: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address",
			msg: MsgSubmitNewParticipant{
				Creator: validCreator,
			},
		}, {
			name: "valid validator key",
			msg: MsgSubmitNewParticipant{
				Creator:      validCreator,
				ValidatorKey: sample.ValidED25519ValidatorKey(),
			},
		}, {
			name: "valid worker key",
			msg: MsgSubmitNewParticipant{
				Creator:   validCreator,
				WorkerKey: sample.ValidSECP256K1AccountKey(),
			},
		}, {
			name: "valid validator and worker keys",
			msg: MsgSubmitNewParticipant{
				Creator:      validCreator,
				ValidatorKey: sample.ValidED25519ValidatorKey(),
				WorkerKey:    sample.ValidSECP256K1AccountKey(),
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
