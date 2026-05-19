package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

const testReceiptsRoot = "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

func TestMsgBridgeExchange_ValidateBasic(t *testing.T) {
	owner := sample.AccAddress()
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
				OwnerAddress:    owner,
				OwnerPubKey:     "pk",
				Amount:          "100",
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    testReceiptsRoot,
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "rejects leading zeros in block number",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    owner,
				OwnerPubKey:     "pk",
				Amount:          "100",
				BlockNumber:     "01",
				ReceiptIndex:    "0",
				ReceiptsRoot:    testReceiptsRoot,
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "valid minimal",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    owner,
				OwnerPubKey:     "pk",
				Amount:          "100",
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    testReceiptsRoot,
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
