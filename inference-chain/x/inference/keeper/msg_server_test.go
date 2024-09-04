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
	return k, setupMsgServerWithKeeper(k), ctx
}

func setupMsgServerWithKeeper(k keeper.Keeper) types.MsgServer {
	return keeper.NewMsgServerImpl(k)
}

func setupKeeperWithBankMock(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context, *keepertest.MockBankEscrowKeeper) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMock(t)
	return k, keeper.NewMsgServerImpl(k), ctx, mock.BankKeeper
}

func TestMsgServer(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	require.NotNil(t, ms)
	require.NotNil(t, ctx)
	require.NotEmpty(t, k)
}
