package keeper_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// On a block with nothing to prune, PruneWithParams reads the pruning state once and params never.
func TestPruneWithParamsReadsStateOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	params, err := k.GetParams(ctx)
	require.NoError(t, err)

	var trace bytes.Buffer
	ctx.MultiStore().SetTracer(&trace)
	require.NoError(t, k.PruneWithParams(ctx, params, 0))
	ctx.MultiStore().SetTracer(nil)

	reads := func(key []byte) int {
		enc := base64.StdEncoding.EncodeToString(key)
		n := 0
		for _, line := range strings.Split(trace.String(), "\n") {
			if strings.Contains(line, `"operation":"read"`) && strings.Contains(line, `"key":"`+enc+`"`) {
				n++
			}
		}
		return n
	}
	require.Equal(t, 1, reads(types.PruningStatePrefix))
	require.Equal(t, 0, reads(types.ParamsKey))
}
