package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSubmitUnitOfComputePriceProposal_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgSubmitUnitOfComputePriceProposal
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgSubmitUnitOfComputePriceProposal{
				Creator: "invalid_address",
				Price:   1,
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address and price",
			msg: MsgSubmitUnitOfComputePriceProposal{
				Creator: sample.AccAddress(),
				Price:   1,
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
