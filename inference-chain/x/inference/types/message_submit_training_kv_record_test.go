package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSubmitTrainingKvRecord_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgSubmitTrainingKvRecord
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgSubmitTrainingKvRecord{
				Creator:     "invalid_address",
				TaskId:      1,
				Participant: sample.AccAddress(),
				Key:         "k",
				Value:       "v",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address and fields",
			msg: MsgSubmitTrainingKvRecord{
				Creator:     sample.AccAddress(),
				TaskId:      1,
				Participant: sample.AccAddress(),
				Key:         "k",
				Value:       "v",
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
