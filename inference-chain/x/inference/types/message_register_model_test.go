package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgRegisterModel_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgRegisterModel
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgRegisterModel{
				Authority:  "invalid_address",
				ProposedBy: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address",
			msg: MsgRegisterModel{
				Authority:  sample.AccAddress(),
				ProposedBy: sample.AccAddress(),
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
