package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgAssignTrainingTask_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgAssignTrainingTask
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgAssignTrainingTask{
				Creator:   "invalid_address",
				TaskId:    1,
				Assignees: []*TrainingTaskAssignee{&TrainingTaskAssignee{Participant: sample.AccAddress(), NodeIds: []string{"n1"}}},
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address and fields",
			msg: MsgAssignTrainingTask{
				Creator:   sample.AccAddress(),
				TaskId:    1,
				Assignees: []*TrainingTaskAssignee{&TrainingTaskAssignee{Participant: sample.AccAddress(), NodeIds: []string{"n1"}}},
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
