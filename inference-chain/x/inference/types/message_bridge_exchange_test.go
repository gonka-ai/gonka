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
			name: "invalid validator",
			msg: MsgBridgeExchange{
				Validator:       "invalid_address",
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          "100",
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    "0xroot",
			},
			err: sdkerrors.ErrInvalidAddress,
		}, {
			name: "valid minimal",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          "100",
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    "0xroot",
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
