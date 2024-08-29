package keeper_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func setupMsgServer(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context) {
	k, ctx := keepertest.InferenceKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func setupKeeperWithBankMock(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context, *keepertest.MockBankEscrowKeeper) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMock(t)
	return k, keeper.NewMsgServerImpl(k), ctx, mock
}

func TestMsgServer(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	require.NotNil(t, ms)
	require.NotNil(t, ctx)
	require.NotEmpty(t, k)
}
