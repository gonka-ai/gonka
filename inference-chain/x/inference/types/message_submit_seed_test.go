package types

import (
	"encoding/base64"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgSubmitSeed_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgSubmitSeed
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgSubmitSeed{
				Creator:    "invalid_address",
				EpochIndex: 1,
				Seed:       1,
				Signature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "invalid epoch index",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 0,
				Seed:       1,
				Signature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "invalid zero seed",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Seed:       0,
				Signature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "invalid negative seed",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Seed:       -10,
				Signature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "valid minimal",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Seed:       1,
				Signature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
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
