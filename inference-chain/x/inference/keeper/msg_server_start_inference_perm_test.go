package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_StartInference_Permissions(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)
	signer := testutil.Creator
	msg := &types.MsgStartInference{Creator: signer}

	// Not a participant -> fail
	err := keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.Error(t, err)

	// Register participant
	p := types.Participant{Index: signer, Address: signer}
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), p))

	// Success
	err = keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.NoError(t, err)
}
