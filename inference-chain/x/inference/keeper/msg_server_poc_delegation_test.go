package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgSetPoCDelegation_ValidateBasic_RejectsSelfDelegation(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	msg := &types.MsgSetPoCDelegation{
		Sender:     testutil.Creator,
		ModelId:    "test-model",
		DelegateTo: testutil.Creator,
	}

	err := msg.ValidateBasic()
	require.ErrorIs(t, err, types.ErrSelfDelegation)
}
