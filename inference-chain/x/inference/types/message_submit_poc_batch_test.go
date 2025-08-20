package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSubmitPocBatch_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgSubmitPocBatch
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgSubmitPocBatch{
				Creator: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address",
			msg: MsgSubmitPocBatch{
				Creator:                  sample.AccAddress(),
				PocStageStartBlockHeight: 1,
				BatchId:                  "b1",
				Nonces:                   []int64{0, 1},
				Dist:                     []float64{0.3, 0.7},
				NodeId:                   "node-1",
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
