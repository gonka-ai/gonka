package keeper_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/restrictions/types"
)

// Once unregistered, the per-block check no longer reads params.
func TestCheckAndUnregisterRestrictionSkipsParamsAfterUnregister(t *testing.T) {
	k, ctx := keepertest.RestrictionsKeeper(t)
	params := types.DefaultParams()
	params.RestrictionEndBlock = 1000
	require.NoError(t, k.SetParams(ctx, params))
	ctx = ctx.WithBlockHeight(2000)
	require.NoError(t, k.CheckAndUnregisterRestriction(ctx))

	var trace bytes.Buffer
	ctx.MultiStore().SetTracer(&trace)
	require.NoError(t, k.CheckAndUnregisterRestriction(ctx))
	ctx.MultiStore().SetTracer(nil)

	enc := base64.StdEncoding.EncodeToString(types.ParamsKey)
	require.NotContains(t, trace.String(), `"key":"`+enc+`"`)
	require.False(t, k.IsRestrictionActive(ctx))
}
