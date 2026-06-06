package app

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/stretchr/testify/require"
)

// stubSimModule is a non-nil AppModuleSimulation stub that returns a
// non-empty WeightedOperations slice. Lets the test below show that
// disabledOpsSimModule's override actually fires, instead of trivially
// passing because of a nil embed.
type stubSimModule struct {
	module.AppModuleSimulation
}

func (stubSimModule) WeightedOperations(_ module.SimulationState) []simtypes.WeightedOperation {
	return make([]simtypes.WeightedOperation, 1)
}

// TestDisabledOpsSimModule_SuppressesEmbeddedOps confirms the wrapper's
// override takes precedence over the embedded module's
// WeightedOperations.
func TestDisabledOpsSimModule_SuppressesEmbeddedOps(t *testing.T) {
	require.NotEmpty(t, stubSimModule{}.WeightedOperations(module.SimulationState{}))
	wrapped := disabledOpsSimModule{stubSimModule{}}
	require.Empty(t, wrapped.WeightedOperations(module.SimulationState{}))
}
