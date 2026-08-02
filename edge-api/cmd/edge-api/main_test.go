package main

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_DerivesRPCURLFromGRPCHost(t *testing.T) {
	// Adding the query fallback must not break a deployment that only sets the
	// gRPC endpoint, so the RPC endpoint is derived from the same host.
	t.Setenv(envChainGRPCURL, "node:9090")
	t.Setenv(envChainRPCURL, "")

	cfg, err := loadConfig()
	require.NoError(t, err)

	assert.Equal(t, "node:9090", cfg.ChainGRPCURL)
	assert.Equal(t, "http://node:26657", cfg.ChainRPCURL)
	assert.True(t, cfg.ChainRPCDerived)
	assert.Equal(t, defaultPort, cfg.Port)
}

func TestLoadConfig_ExplicitRPCURLWins(t *testing.T) {
	t.Setenv(envChainGRPCURL, "node:9090")
	t.Setenv(envChainRPCURL, "http://other:36657")

	cfg, err := loadConfig()
	require.NoError(t, err)

	assert.Equal(t, "http://other:36657", cfg.ChainRPCURL)
	assert.False(t, cfg.ChainRPCDerived)
}

func TestLoadConfig_RequiresGRPCURL(t *testing.T) {
	t.Setenv(envChainGRPCURL, "")
	t.Setenv(envChainRPCURL, "http://node:26657")

	_, err := loadConfig()
	require.ErrorContains(t, err, envChainGRPCURL)
}

func TestLoadConfig_RejectsUnparsablePort(t *testing.T) {
	t.Setenv(envChainGRPCURL, "node:9090")
	t.Setenv(envPort, "not-a-port")

	_, err := loadConfig()
	require.ErrorContains(t, err, envPort)
}

func TestLoadConfig_ReadsPort(t *testing.T) {
	t.Setenv(envChainGRPCURL, "node:9090")
	t.Setenv(envPort, "19090")

	cfg, err := loadConfig()
	require.NoError(t, err)
	assert.Equal(t, 19090, cfg.Port)
}

func TestLoadConfig_ShutdownDefaults(t *testing.T) {
	t.Setenv(envChainGRPCURL, "node:9090")

	cfg, err := loadConfig()
	require.NoError(t, err)

	// The announce window must outlast the balancer's 1s health-check interval,
	// and the budget must cover the read timeout of the hop in front.
	assert.Equal(t, defaultDrainAnnounce, cfg.DrainAnnounce)
	assert.Equal(t, defaultShutdownBudget, cfg.ShutdownBudget)
}

func TestLoadConfig_ReadsShutdownDurations(t *testing.T) {
	t.Setenv(envChainGRPCURL, "node:9090")
	t.Setenv(envDrainAnnounce, "0s")
	t.Setenv(envShutdownBudget, "45s")

	cfg, err := loadConfig()
	require.NoError(t, err)

	// Zero is legitimate: it means nothing is health-checking this instance.
	assert.Equal(t, time.Duration(0), cfg.DrainAnnounce)
	assert.Equal(t, 45*time.Second, cfg.ShutdownBudget)
}

func TestLoadConfig_RejectsBadShutdownDurations(t *testing.T) {
	// A typo in a shutdown budget must fail at boot, not during an outage.
	for _, tc := range []struct{ name, key, value string }{
		{"unparsable announce", envDrainAnnounce, "5"},
		{"unparsable budget", envShutdownBudget, "forever"},
		{"negative announce", envDrainAnnounce, "-1s"},
		{"zero budget", envShutdownBudget, "0s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envChainGRPCURL, "node:9090")
			t.Setenv(tc.key, tc.value)

			_, err := loadConfig()
			require.ErrorContains(t, err, tc.key)
		})
	}
}

func TestAwaitDrainAnnouncement_WaitsOutTheWindow(t *testing.T) {
	start := time.Now()
	awaitDrainAnnouncement(75*time.Millisecond, make(chan os.Signal))
	assert.GreaterOrEqual(t, time.Since(start), 75*time.Millisecond)
}

func TestAwaitDrainAnnouncement_SecondSignalCutsItShort(t *testing.T) {
	force := make(chan os.Signal, 1)
	force <- syscall.SIGTERM

	start := time.Now()
	awaitDrainAnnouncement(30*time.Second, force)
	assert.Less(t, time.Since(start), 5*time.Second,
		"a second signal must not wait out the announce window")
}

func TestAwaitDrainAnnouncement_ZeroWindowReturnsImmediately(t *testing.T) {
	start := time.Now()
	awaitDrainAnnouncement(0, make(chan os.Signal))
	assert.Less(t, time.Since(start), time.Second)
}
