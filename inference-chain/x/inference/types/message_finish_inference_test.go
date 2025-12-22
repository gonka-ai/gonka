package types

import (
	"encoding/base64"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgFinishInference_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgFinishInference
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgFinishInference{
				Creator:              "invalid_address",
				ExecutedBy:           sample.AccAddress(),
				TransferredBy:        sample.AccAddress(),
				RequestedBy:          sample.AccAddress(),
				InferenceId:          base64.StdEncoding.EncodeToString(make([]byte, 64)),
				ResponseHash:         "rh",
				PromptHash:           "ph",
				OriginalPromptHash:   "oph",
				Model:                "m",
				RequestTimestamp:     1,
				TransferSignature:    base64.StdEncoding.EncodeToString(make([]byte, 64)),
				ExecutorSignature:    base64.StdEncoding.EncodeToString(make([]byte, 64)),
				PromptTokenCount:     0,
				CompletionTokenCount: 0,
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address",
			msg: MsgFinishInference{
				Creator:              sample.AccAddress(),
				ExecutedBy:           sample.AccAddress(),
				TransferredBy:        sample.AccAddress(),
				RequestedBy:          sample.AccAddress(),
				InferenceId:          base64.StdEncoding.EncodeToString(make([]byte, 64)),
				ResponseHash:         "rh",
				PromptHash:           "ph",
				OriginalPromptHash:   "oph",
				Model:                "m",
				RequestTimestamp:     1,
				TransferSignature:    base64.StdEncoding.EncodeToString(make([]byte, 64)),
				ExecutorSignature:    base64.StdEncoding.EncodeToString(make([]byte, 64)),
				PromptTokenCount:     0,
				CompletionTokenCount: 0,
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
