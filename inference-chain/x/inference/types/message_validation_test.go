package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgValidation_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgValidation
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgValidation{
				Creator:         "invalid_address",
				Id:              "id",
				InferenceId:     "iid",
				ResponsePayload: "payload",
				ResponseHash:    "hash",
				Value:           0.5,
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address & all required fields",
			msg: MsgValidation{
				Creator:         sample.AccAddress(),
				Id:              "id",
				InferenceId:     "iid",
				ResponsePayload: "payload",
				ResponseHash:    "hash",
				Value:           0.5,
			},
		},
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
