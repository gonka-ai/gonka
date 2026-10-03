package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// descMeter counts gas charges by descriptor.
type descMeter struct {
	storetypes.GasMeter
	byDesc map[string]int
}

func (m *descMeter) ConsumeGas(a storetypes.Gas, d string) {
	m.byDesc[d]++
	m.GasMeter.ConsumeGas(a, d)
}

// The active confirmation PoC event costs one store read, with or without an event.
func TestGetActiveConfirmationPoCEvent_OneRead(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	read := func() (*types.ConfirmationPoCEvent, bool, map[string]int) {
		m := &descMeter{GasMeter: storetypes.NewInfiniteGasMeter(), byDesc: map[string]int{}}
		event, ok, err := k.GetActiveConfirmationPoCEvent(ctx.WithGasMeter(m))
		require.NoError(t, err)
		return event, ok, m.byDesc
	}

	event, ok, gas := read()
	require.False(t, ok)
	require.Nil(t, event)
	require.Equal(t, 1, gas[storetypes.GasReadCostFlatDesc]+gas[storetypes.GasHasDesc])

	want := types.ConfirmationPoCEvent{EpochIndex: 3, EventSequence: 1, TriggerHeight: 90, GenerationStartHeight: 100}
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, want))
	event, ok, gas = read()
	require.True(t, ok)
	require.Equal(t, want, *event)
	require.Equal(t, 0, gas[storetypes.GasHasDesc])
	require.Equal(t, 1, gas[storetypes.GasReadCostFlatDesc])
}
