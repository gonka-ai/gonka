package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, chainREST, defaultModel string, perf *PerfTracker) (*devshardRuntime, error) {
		switch cfg.ID {
		case "12":
			return nil, fmt.Errorf("runtime %s: create session: build group: get escrow: %w", cfg.ID, bridge.ErrEscrowNotFound)
		case "24":
			return &devshardRuntime{id: cfg.ID, model: defaultModel}, nil
		default:
			return nil, fmt.Errorf("unexpected runtime id %s", cfg.ID)
		}
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	runtimes, err := buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil))
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
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, chainREST, defaultModel string, perf *PerfTracker) (*devshardRuntime, error) {
		switch cfg.ID {
		case "12":
			return nil, fmt.Errorf("runtime %s: %w", cfg.ID, errRuntimePrivateKeyMissing)
		case "24":
			return &devshardRuntime{id: cfg.ID, model: defaultModel}, nil
		default:
			return nil, fmt.Errorf("unexpected runtime id %s", cfg.ID)
		}
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	runtimes, err := buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil))
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
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, chainREST, defaultModel string, perf *PerfTracker) (*devshardRuntime, error) {
		return nil, fmt.Errorf("runtime %s: create session: dial tcp timeout", cfg.ID)
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	_, err = buildGatewayRuntimes(store, &state, t.TempDir(), NewPerfTracker(nil))
	require.Error(t, err)

	reloaded, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, reloaded.Devshards[0].Active)
}

func TestRepairPersistedGatewayEndpointSettingsBackfillsBlankPublicAPI(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
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
	mustRepairPersistedGatewayEndpointSettings(store, &state, cliFlags{
		chainREST: defaultChainRESTURL,
		publicAPI: defaultPublicAPIURL,
	})

	require.Equal(t, "http://api:9000", state.Settings.PublicAPI)

	reloaded, ok := reloadGatewayStateForTest(t, store)
	require.True(t, ok)
	require.Equal(t, "http://api:9000", reloaded.Settings.PublicAPI)
	require.Equal(t, "http://node:1317", reloaded.Settings.ChainREST)
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
		chainREST: defaultChainRESTURL,
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
