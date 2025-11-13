package types

import (
	"testing"

	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgUpdateParams_ValidateBasic(t *testing.T) {
	tests := []struct {
		name        string
		msg         MsgUpdateParams
		expectError bool
	}{
		{
			name: "invalid authority address",
			msg: MsgUpdateParams{
				Authority: "invalid_address",
				Params:   DefaultParams(),
			},
			expectError: true,
		}, {
			name: "valid address with default params",
			msg: MsgUpdateParams{
				Authority: sample.AccAddress(),
				Params:   DefaultParams(),
			},
			expectError: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.ValidateBasic()
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

