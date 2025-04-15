package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgBridgeExchange_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgBridgeExchange
		err  error
	}{
		{
			name: "invalid address",
			msg: MsgBridgeExchange{
				Validator: "invalid_address",
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "valid address but missing fields",
			msg: MsgBridgeExchange{
				Validator: sample.AccAddress(),
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "valid message",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0x1234567890123456789012345678901234567890",
				OwnerAddress:    "0x1234567890123456789012345678901234567890",
				Amount:          "1000000",
				TransactionHash: "0x1234567890123456789012345678901234567890123456789012345678901234",
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
