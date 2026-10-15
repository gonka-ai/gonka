package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE must win on every start, not only when
// the settings row is first created (#2016).
func TestBootstrapEnvOverrideAppliesOnRestart(t *testing.T) {
	for _, backend := range gatewayStoreTestBackends {
		t.Run(backend, func(t *testing.T) {
			store := newTestGatewayStore(t, backend)
			ctx := context.Background()

			off := false
			require.NoError(t, store.Initialize(ctx, GatewaySettings{DefaultModel: "m", LogprobsOptimizationOverride: &off}, nil))

			// The operator restarts with the env flipped to true.
			on := true
			require.NoError(t, applyBootstrapEnvOverrides(ctx, store, GatewaySettings{DefaultModel: "m", LogprobsOptimizationOverride: &on}))
			state, ok, err := store.LoadState(ctx)
			require.NoError(t, err)
			require.True(t, ok)
			require.NotNil(t, state.Settings.LogprobsOptimizationOverride)
			require.True(t, *state.Settings.LogprobsOptimizationOverride)
			require.Equal(t, "m", state.Settings.DefaultModel, "the other settings survive the round trip")

			// An unset env leaves the stored value alone.
			require.NoError(t, applyBootstrapEnvOverrides(ctx, store, GatewaySettings{DefaultModel: "m"}))
			state, _, err = store.LoadState(ctx)
			require.NoError(t, err)
			require.True(t, *state.Settings.LogprobsOptimizationOverride)
		})
	}
}
