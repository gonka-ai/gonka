package inference

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// Epoch formation reads params, the effective epoch index and the current root
// epoch group data from the store once for the whole EndBlock.
func TestEndBlockEpochFormationReadsParamsOnce(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 2)
	fixture.addFreshPoC(t, 100)
	params, err := fixture.keeper.GetParams(fixture.ctx)
	require.NoError(t, err)
	ec, err := types.NewEpochContextFromEffectiveEpoch(fixture.currentEpoch, *params.EpochParams, 0)
	require.NoError(t, err)
	height := ec.EndOfPoCValidation()
	require.True(t, ec.IsEndOfPoCValidationStage(height))
	blockCtx := fixture.ctx.WithBlockHeight(height)

	var trace bytes.Buffer
	blockCtx.MultiStore().SetTracer(&trace)
	require.NoError(t, fixture.module.EndBlock(blockCtx))
	blockCtx.MultiStore().SetTracer(nil)

	stored, found := fixture.keeper.GetActiveParticipants(blockCtx, fixture.upcomingEpoch.Index)
	require.True(t, found, "the epoch was formed")
	require.NotEmpty(t, stored.Participants)

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
	require.Equal(t, 1, reads(types.ParamsKey))
	require.Equal(t, 1, reads(types.EffectiveEpochIndexPrefix))
	m := fixture.keeper.EpochGroupDataMap
	rootKey, err := collections.EncodeKeyWithPrefix(m.GetPrefix(), m.KeyCodec(), collections.Join(fixture.currentEpoch.Index, ""))
	require.NoError(t, err)
	require.Equal(t, 1, reads(rootKey), "current root epoch group data")
}
