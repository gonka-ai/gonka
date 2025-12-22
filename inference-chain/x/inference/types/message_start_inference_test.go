package types

import (
	"encoding/base64"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgStartInference_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgStartInference
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgStartInference{
				Creator:            "invalid_address",
				RequestedBy:        sample.AccAddress(),
				AssignedTo:         sample.AccAddress(),
				InferenceId:        base64.StdEncoding.EncodeToString(make([]byte, 64)),
				PromptHash:         "hash",
				OriginalPromptHash: "orig_hash",
				Model:              "model-x",
				NodeVersion:        "v1",
				RequestTimestamp:   1,
				TransferSignature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid address",
			msg: MsgStartInference{
				Creator:            sample.AccAddress(),
				RequestedBy:        sample.AccAddress(),
				AssignedTo:         sample.AccAddress(),
				InferenceId:        base64.StdEncoding.EncodeToString(make([]byte, 64)),
				PromptHash:         "hash",
				OriginalPromptHash: "orig_hash",
				Model:              "model-x",
				NodeVersion:        "v1",
				RequestTimestamp:   1,
				TransferSignature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
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
