package types

import (
	"strings"
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
			name: "amount at uint256 max",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          "115792089237316195423570985008687907853269984665640564039457584007913129639935",
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    "0xroot",
			},
		}, {
			name: "amount 2^256 within digit limit",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          "115792089237316195423570985008687907853269984665640564039457584007913129639936",
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    "0xroot",
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "amount at digit limit above uint256",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          strings.Repeat("9", MaxBridgeAmountDigits),
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    "0xroot",
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "amount over digit limit",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          strings.Repeat("9", MaxBridgeAmountDigits+1),
				BlockNumber:     "1",
				ReceiptIndex:    "0",
				ReceiptsRoot:    "0xroot",
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "blockNumber over digit limit",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          "100",
				BlockNumber:     strings.Repeat("1", MaxBridgeAmountDigits+1),
				ReceiptIndex:    "0",
				ReceiptsRoot:    "0xroot",
			},
			err: sdkerrors.ErrInvalidRequest,
		}, {
			name: "receiptIndex over digit limit",
			msg: MsgBridgeExchange{
				Validator:       sample.AccAddress(),
				OriginChain:     "ethereum",
				ContractAddress: "0xabc",
				OwnerAddress:    "0xowner",
				OwnerPubKey:     "pk",
				Amount:          "100",
				BlockNumber:     "1",
				ReceiptIndex:    strings.Repeat("1", MaxBridgeAmountDigits+1),
				ReceiptsRoot:    "0xroot",
			},
			err: sdkerrors.ErrInvalidRequest,
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
