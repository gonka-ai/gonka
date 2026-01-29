package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/genesistransfer/types"
)

func TestGetParams(t *testing.T) {
	k, ctx := keepertest.GenesistransferKeeper(t)
	expectedParams := types.DefaultParams()

	require.NoError(t, k.SetParams(ctx, expectedParams))
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.EqualValues(t, expectedParams, params)
}
