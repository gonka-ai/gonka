package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgCreateDummyTrainingTask_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgCreateDummyTrainingTask
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgCreateDummyTrainingTask{
				Creator: "invalid_address",
				Task:    &TrainingTask{HardwareResources: []*TrainingHardwareResources{{Type: "GPU", Count: 1}}},
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid minimal",
			msg: MsgCreateDummyTrainingTask{
				Creator: sample.AccAddress(),
				Task:    &TrainingTask{HardwareResources: []*TrainingHardwareResources{{Type: "GPU", Count: 1}}, Assignees: []*TrainingTaskAssignee{{Participant: sample.AccAddress(), NodeIds: []string{"n1"}}}},
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
