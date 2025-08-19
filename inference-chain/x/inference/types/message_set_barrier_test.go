package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSetBarrier_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgSetBarrier
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgSetBarrier{
				Creator: "invalid_address",
				Req:     &SetBarrierRequest{BarrierId: "b1", NodeId: "n1", RunId: 1, OuterStep: 0},
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid",
			msg: MsgSetBarrier{
				Creator: sample.AccAddress(),
				Req:     &SetBarrierRequest{BarrierId: "b1", NodeId: "n1", RunId: 1, OuterStep: 0},
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
