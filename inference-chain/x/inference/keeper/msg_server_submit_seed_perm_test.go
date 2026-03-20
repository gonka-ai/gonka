package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_SubmitSeed_Permissions(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)

	signer := testutil.Creator
	msg := &types.MsgSubmitSeed{Creator: signer}

	// Unregistered participant should fail
	err := keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.Error(t, err)

	// Register participant and retry -> should pass
	p := types.Participant{Index: signer, Address: signer}
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), p))
	err = keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.NoError(t, err)
}
