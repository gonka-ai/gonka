package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgClaimTrainingTaskForAssignment_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgClaimTrainingTaskForAssignment
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgClaimTrainingTaskForAssignment{
				Creator: "invalid_address",
				TaskId:  1,
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address and task_id",
			msg: MsgClaimTrainingTaskForAssignment{
				Creator: sample.AccAddress(),
				TaskId:  1,
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
