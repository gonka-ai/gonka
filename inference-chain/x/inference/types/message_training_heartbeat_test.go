package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgTrainingHeartbeat_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgTrainingHeartbeat
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgTrainingHeartbeat{
				Creator: "invalid_address",
				Req:     &HeartbeatRequest{NodeId: "n1", RunId: 1, LocalRank: 0, Timestamp: 0, InnerStep: 0, OuterStep: 0, Epoch: 0},
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid",
			msg: MsgTrainingHeartbeat{
				Creator: sample.AccAddress(),
				Req:     &HeartbeatRequest{NodeId: "n1", RunId: 1, LocalRank: 0, Timestamp: 0, InnerStep: 0, OuterStep: 0, Epoch: 0},
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
