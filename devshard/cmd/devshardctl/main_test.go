package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
)

func TestBootstrapEscrowRotationSettlementEnabledEnv(t *testing.T) {
	t.Setenv("DEVSHARDS_JSON", "[]")
	t.Setenv("DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED", "true")
	opts := mustLoadBootstrapOptions(cliFlags{}, t.TempDir())
	require.True(t, opts.bootstrapSettings.EscrowRotation.SettlementEnabled)
}

func TestBootstrapEscrowRotationSettlementDefaultsDisabled(t *testing.T) {
	t.Setenv("DEVSHARDS_JSON", "[]")
	require.NoError(t, os.Unsetenv("DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED"))
	opts := mustLoadBootstrapOptions(cliFlags{}, t.TempDir())
	require.False(t, opts.bootstrapSettings.EscrowRotation.SettlementEnabled)
}

func TestBuildGatewayRuntimesDeactivatesMissingEscrow(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
		{RuntimeConfig: RuntimeConfig{ID: "24", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		switch cfg.ID {
		case "12":
			return nil, fmt.Errorf("runtime %s: create session: build group: get escrow: %w", cfg.ID, bridge.ErrEscrowNotFound)
		case "24":
			return &devshardRuntime{id: cfg.ID, model: deps.defaultModel}, nil
		default:
			return nil, fmt.Errorf("unexpected runtime id %s", cfg.ID)
		}
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	runtimes, err := buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, "24", runtimes[0].id)
	require.False(t, state.Devshards[0].Active)
	require.True(t, state.Devshards[1].Active)

	reloaded, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, reloaded.Devshards[0].Active)
	require.True(t, reloaded.Devshards[1].Active)
}

func TestBuildGatewayRuntimesDeactivatesMissingPrivateKey(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyEnv: "DEVSHARD_12_PRIVATE_KEY", Model: "Qwen/Test"}, Active: true},
		{RuntimeConfig: RuntimeConfig{ID: "24", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		switch cfg.ID {
		case "12":
			return nil, fmt.Errorf("runtime %s: %w", cfg.ID, errRuntimePrivateKeyMissing)
		case "24":
			return &devshardRuntime{id: cfg.ID, model: deps.defaultModel}, nil
		default:
			return nil, fmt.Errorf("unexpected runtime id %s", cfg.ID)
		}
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	runtimes, err := buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, "24", runtimes[0].id)
	require.False(t, state.Devshards[0].Active)
	require.True(t, state.Devshards[1].Active)

	reloaded, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, reloaded.Devshards[0].Active)
	require.True(t, reloaded.Devshards[1].Active)
}

func TestBuildGatewayRuntimesPreservesActiveOnOtherErrors(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		return nil, fmt.Errorf("runtime %s: create session: dial tcp timeout", cfg.ID)
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	_, err = buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))
	require.Error(t, err)

	reloaded, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, reloaded.Devshards[0].Active)
}

// measurePeakRuntimeBuildConcurrency runs buildGatewayRuntimes over n active
// devshards with a fake builder that records the peak number of simultaneous
// builder invocations.
func measurePeakRuntimeBuildConcurrency(t *testing.T, n int) int64 {
	t.Helper()
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, activeDevshardStates(n)))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	var inFlight, peakInFlight atomic.Int64
	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		running := inFlight.Add(1)
		for {
			peak := peakInFlight.Load()
			if running <= peak || peakInFlight.CompareAndSwap(peak, running) {
				break
			}
		}
		// Hold the slot briefly so overlapping builders show up in the
		// high-water mark; this occupies the resource, it does not wait on
		// async work.
		time.Sleep(25 * time.Millisecond)
		inFlight.Add(-1)
		return &devshardRuntime{id: cfg.ID, model: deps.defaultModel}, nil
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = savedBuilder })

	runtimes, err := buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))
	require.NoError(t, err)
	require.Len(t, runtimes, n)
	return peakInFlight.Load()
}

func TestBuildGatewayRuntimesBoundsBuilderConcurrency(t *testing.T) {
	// An unbounded fan-out floods the chain (429) and crash-loops startup;
	// builder concurrency must stay within the resolved bound.
	const devshardCount = 32
	limit := int64(resolveMaxConcurrentRuntimeBuilds())

	peak := measurePeakRuntimeBuildConcurrency(t, devshardCount)

	require.Greater(t, int64(devshardCount), limit, "test needs more devshards than the bound to be meaningful")
	require.LessOrEqual(t, peak, limit, "runtime builder fan-out is unbounded: %d builders ran concurrently", peak)
}

func TestBuildGatewayRuntimesHonorsConcurrencyEnvOverride(t *testing.T) {
	t.Setenv("DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS", "4")

	peak := measurePeakRuntimeBuildConcurrency(t, 32)

	require.LessOrEqual(t, peak, int64(4), "DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS=4 must bound builder concurrency")
}

func activeDevshardStates(n int) []GatewayDevshardState {
	states := make([]GatewayDevshardState, 0, n)
	for i := 0; i < n; i++ {
		states = append(states, GatewayDevshardState{
			RuntimeConfig: RuntimeConfig{ID: fmt.Sprintf("%d", i+1), PrivateKeyHex: "secret", Model: "Qwen/Test"},
			Active:        true,
		})
	}
	return states
}

func TestBuildGatewayRuntimesBoundedFanoutSurvivesRateLimitingLCD(t *testing.T) {
	// Regression: an unbounded builder fan-out floods the chain, which
	// answers 429. A 429 is not a soft error (unlike ErrEscrowNotFound /
	// private-key-missing), so it becomes firstFatal and the gateway fails to
	// start — restart repeats the same fan-out — crash loop. With the fan-out
	// bounded, at most the resolved limit of builders hit the chain at once, so a
	// backend that tolerates that many never rate-limits and startup succeeds.
	const devshardCount = 32
	limit := int64(resolveMaxConcurrentRuntimeBuilds())

	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, activeDevshardStates(devshardCount)))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	// The fake chain tolerates up to the resolved limit of concurrent builds and
	// rejects the one that exceeds it with a 429.
	var inFlight atomic.Int64
	var rateLimited atomic.Bool
	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		defer inFlight.Add(-1)
		if inFlight.Add(1) > limit {
			rateLimited.Store(true)
			return nil, fmt.Errorf("runtime %s: get escrow: chain: 429 Too Many Requests", cfg.ID)
		}
		// Hold the slot so genuinely concurrent builders overlap.
		time.Sleep(25 * time.Millisecond)
		return &devshardRuntime{id: cfg.ID, model: deps.defaultModel}, nil
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = savedBuilder })

	runtimes, err := buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))

	require.False(t, rateLimited.Load(), "fan-out exceeded the chain's concurrency tolerance and tripped a 429")
	require.NoError(t, err, "bounded startup must survive a rate-limiting chain")
	require.Len(t, runtimes, devshardCount)
}

func TestResolveMaxConcurrentRuntimeBuildsDefaultsWhenUnset(t *testing.T) {
	require.NoError(t, os.Unsetenv("DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS"))
	require.Equal(t, defaultMaxConcurrentRuntimeBuilds, resolveMaxConcurrentRuntimeBuilds())
}

func TestResolveMaxConcurrentRuntimeBuildsParsesOverride(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{name: "valid positive", env: "4", want: 4},
		{name: "zero falls back", env: "0", want: defaultMaxConcurrentRuntimeBuilds},
		{name: "negative falls back", env: "-3", want: defaultMaxConcurrentRuntimeBuilds},
		{name: "non-numeric falls back", env: "abc", want: defaultMaxConcurrentRuntimeBuilds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS", tc.env)
			require.Equal(t, tc.want, resolveMaxConcurrentRuntimeBuilds())
		})
	}
}

func TestRepairPersistedGatewayEndpointSettingsBackfillsBlankPublicAPI(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainGRPC:               "",
		PublicAPI:               "",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	t.Setenv("DEVSHARD_PUBLIC_API", "http://api:9000")
	t.Setenv("DEVSHARD_CHAIN_GRPC", "mock-chain:19090")
	mustRepairPersistedGatewayEndpointSettings(store, &state, cliFlags{
		chainGRPC: defaultChainGRPCURL,
		publicAPI: defaultPublicAPIURL,
	})

	require.Equal(t, "http://api:9000", state.Settings.PublicAPI)
	require.Equal(t, "mock-chain:19090", state.Settings.ChainGRPC)

	reloaded, ok := reloadGatewayStateForTest(t, store)
	require.True(t, ok)
	require.Equal(t, "http://api:9000", reloaded.Settings.PublicAPI)
	// chain_grpc is runtime-only until gateway.db schema migration; startup uses env/flags.
	require.Empty(t, reloaded.Settings.ChainGRPC)
}

func TestRepairPersistedGatewayEndpointSettingsPreservesConfiguredPublicAPI(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://configured-api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	t.Setenv("DEVSHARD_PUBLIC_API", "http://env-api:9000")
	mustRepairPersistedGatewayEndpointSettings(store, &state, cliFlags{
		publicAPI: defaultPublicAPIURL,
	})

	require.Equal(t, "http://configured-api:9000", state.Settings.PublicAPI)

	reloaded, ok := reloadGatewayStateForTest(t, store)
	require.True(t, ok)
	require.Equal(t, "http://configured-api:9000", reloaded.Settings.PublicAPI)
}

func reloadGatewayStateForTest(t *testing.T, store *GatewayStore) (GatewayState, bool) {
	t.Helper()
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	return state, ok
}
