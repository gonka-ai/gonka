package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgJoinTrainingStatus_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgJoinTrainingStatus
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgJoinTrainingStatus{
				Creator: "invalid_address",
				Req:     &JoinTrainingRequest{NodeId: "node-1", RunId: 1, OuterStep: 0},
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address and req",
			msg: MsgJoinTrainingStatus{
				Creator: sample.AccAddress(),
				Req:     &JoinTrainingRequest{NodeId: "node-1", RunId: 1, OuterStep: 0},
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
