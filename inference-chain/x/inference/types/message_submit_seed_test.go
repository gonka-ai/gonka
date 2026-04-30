package types

import (
	"encoding/hex"
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
				Signature:  hex.EncodeToString(make([]byte, 64)),
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "invalid signature - not hex",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Signature:  "not_a_hex_string_of_length_128__________________________________________________________________________________________________",
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "invalid signature - wrong length",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Signature:  hex.EncodeToString(make([]byte, 32)),
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "valid minimal",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Signature:  hex.EncodeToString(make([]byte, 64)),
			},
		}, {
			name: "invalid signature - not hex chars",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Signature:  "ZZ" + hex.EncodeToString(make([]byte, 63)),
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "invalid signature - odd length hex",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 1,
				Signature:  "abc",
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "invalid epoch index - zero",
			msg: MsgSubmitSeed{
				Creator:    sample.AccAddress(),
				EpochIndex: 0,
				Signature:  hex.EncodeToString(make([]byte, 64)),
			},
			err: sdkerrors.ErrInvalidRequest,
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
