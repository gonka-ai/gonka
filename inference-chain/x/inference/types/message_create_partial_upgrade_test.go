package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgCreatePartialUpgrade_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgCreatePartialUpgrade
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgCreatePartialUpgrade{
				Authority:       "invalid_address",
				Height:          1,
				NodeVersion:     "v1",
				ApiBinariesJson: "{}",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address and fields",
			msg: MsgCreatePartialUpgrade{
				Authority:       sample.AccAddress(),
				Height:          1,
				NodeVersion:     "v1",
				ApiBinariesJson: "{}",
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
