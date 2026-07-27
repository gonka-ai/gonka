package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgSetPoCDelegation_ValidateBasic_RejectsSelfDelegation(t *testing.T) {
	keepertest.EnsureBech32Config()

	msg := &types.MsgSetPoCDelegation{
		Sender:     testutil.Creator,
		ModelId:    "test-model",
		DelegateTo: testutil.Creator,
	}

	err := msg.ValidateBasic()
	require.ErrorIs(t, err, types.ErrSelfDelegation)
}
