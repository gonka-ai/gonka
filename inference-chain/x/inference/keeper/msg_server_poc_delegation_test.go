package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
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

func TestPoCDelegationValidateBasic_RejectsUppercaseSender(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	upper := strings.ToUpper(testutil.Creator)

	err := (&types.MsgRefusePoCDelegation{Sender: upper, ModelId: "test-model"}).ValidateBasic()
	require.ErrorIs(t, err, sdkerrors.ErrInvalidAddress)

	err = (&types.MsgDeclarePoCIntent{Sender: upper, ModelId: "test-model"}).ValidateBasic()
	require.ErrorIs(t, err, sdkerrors.ErrInvalidAddress)

	require.NoError(t, (&types.MsgRefusePoCDelegation{Sender: testutil.Creator, ModelId: "test-model"}).ValidateBasic())
	require.NoError(t, (&types.MsgDeclarePoCIntent{Sender: testutil.Creator, ModelId: "test-model"}).ValidateBasic())
}
