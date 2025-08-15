package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgInvalidateInference_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgInvalidateInference
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgInvalidateInference{
				Creator: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address",
			msg: MsgInvalidateInference{
				Creator:     sample.AccAddress(),
				InferenceId: "iid",
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
