package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func TestGetParams(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)
	params := types.DefaultParams()

	require.NoError(t, k.SetParams(ctx, params))
	outParams, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.EqualValues(t, params, outParams)
}
