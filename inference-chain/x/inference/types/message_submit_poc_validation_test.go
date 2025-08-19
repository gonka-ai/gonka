package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSubmitPocValidation_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgSubmitPocValidation
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgSubmitPocValidation{
				Creator: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address",
			msg: MsgSubmitPocValidation{
				Creator:                  sample.AccAddress(),
				ParticipantAddress:       sample.AccAddress(),
				PocStageStartBlockHeight: 1,
				Nonces:                   []int64{0, 1, 2},
				Dist:                     []float64{0.2, 0.3, 0.5},
				ReceivedDist:             []float64{0.2, 0.3, 0.5},
				RTarget:                  0.5,
				FraudThreshold:           0.9,
				NInvalid:                 0,
				ProbabilityHonest:        0.8,
				FraudDetected:            false,
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
