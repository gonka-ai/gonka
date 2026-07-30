package types

import (
	"strings"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgRequestBridgeWithdrawalAmountLength(t *testing.T) {
	newMessage := func(amount string) *MsgRequestBridgeWithdrawal {
		return &MsgRequestBridgeWithdrawal{
			Creator:                  sample.AccAddress(),
			UserAddress:              sample.AccAddress(),
			Amount:                   amount,
			DestinationAddress:       "0x1111111111111111111111111111111111111111",
			DestinationBridgeAddress: "0x2222222222222222222222222222222222222222",
		}
	}

	// max uint256 (78 digits) is the largest valid amount
	maxUint256 := "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	require.Len(t, maxUint256, MaxBridgeAmountDigits)
	require.NoError(t, newMessage(maxUint256).ValidateBasic())

	// at the digit limit but over 2^256-1: rejected by the parse, not the cap
	require.ErrorIs(t,
		newMessage(strings.Repeat("9", MaxBridgeAmountDigits)).ValidateBasic(),
		sdkerrors.ErrInvalidRequest,
	)

	// over the digit limit: rejected by the cap before parsing
	require.ErrorIs(t,
		newMessage(strings.Repeat("9", MaxBridgeAmountDigits+1)).ValidateBasic(),
		sdkerrors.ErrInvalidRequest,
	)
}
