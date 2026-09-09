package types

import (
	"strings"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgRequestBridgeMintAmountLength(t *testing.T) {
	newMessage := func(amount string) *MsgRequestBridgeMint {
		return &MsgRequestBridgeMint{
			Creator:                  sample.AccAddress(),
			Amount:                   amount,
			DestinationAddress:       "0x1111111111111111111111111111111111111111",
			ChainId:                  "ethereum",
			DestinationBridgeAddress: "0x2222222222222222222222222222222222222222",
		}
	}

	require.NoError(t, newMessage(strings.Repeat("9", MaxBridgeAmountDigits)).ValidateBasic())
	require.ErrorIs(t,
		newMessage(strings.Repeat("9", MaxBridgeAmountDigits+1)).ValidateBasic(),
		sdkerrors.ErrInvalidRequest,
	)
}
