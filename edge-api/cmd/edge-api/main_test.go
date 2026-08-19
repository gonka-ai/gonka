package main

import (
	"os"
	"regexp"
	"strconv"
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

	assert.Equal(t, 5*time.Second, cfg.DrainAnnounce)
	assert.Equal(t, 2*time.Minute, cfg.ShutdownBudget)
}

func TestShutdownDefaultsCoverRouterContract(t *testing.T) {
	template := readRouterContractFile(t, "../../../edge-api-router/haproxy.cfg.template")
	entrypoint := readRouterContractFile(t, "../../../edge-api-router/entrypoint.sh")

	checkTimeout := routerDuration(t, template,
		regexp.MustCompile(`(?m)^[\t ]*timeout check ([0-9]+m?s)`))
	checkInterval := routerDuration(t, template,
		regexp.MustCompile(`(?m)^[\t ]*server-template [^\n]* inter ([0-9]+m?s)`))
	fall := routerInteger(t, template,
		regexp.MustCompile(`(?m)^[\t ]*server-template [^\n]* fall ([0-9]+)`))
	const observationMargin = time.Second
	worstDetection := checkTimeout + checkInterval*time.Duration(fall)
	require.GreaterOrEqual(t, minDrainAnnounce, worstDetection+observationMargin,
		"minimum announce must cover the router health-check failure window")
	require.GreaterOrEqual(t, defaultDrainAnnounce, minDrainAnnounce,
		"announce default must not bypass the minimum")

	readTimeoutSeconds := routerInteger(t, entrypoint,
		regexp.MustCompile(`READ_TIMEOUT="\$\{EDGE_API_ROUTER_READ_TIMEOUT_SECONDS:-([0-9]+)\}"`))
	require.Equal(t, time.Duration(readTimeoutSeconds)*time.Second, defaultShutdownBudget,
		"shutdown default must match the router's response inactivity timeout")
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
		{"short announce", envDrainAnnounce, "4s"},
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

func TestLoadConfig_AcceptsDrainAnnounceBoundary(t *testing.T) {
	t.Setenv(envChainGRPCURL, "node:9090")
	t.Setenv(envDrainAnnounce, minDrainAnnounce.String())

	cfg, err := loadConfig()
	require.NoError(t, err)
	assert.Equal(t, minDrainAnnounce, cfg.DrainAnnounce)
}

func TestAwaitDrainAnnouncement_WaitsOutTheWindow(t *testing.T) {
	start := time.Now()
	sig := awaitDrainAnnouncement(75*time.Millisecond, make(chan os.Signal))
	assert.Nil(t, sig)
	assert.GreaterOrEqual(t, time.Since(start), 75*time.Millisecond)
}

func TestAwaitDrainAnnouncement_ReturnsSecondSignal(t *testing.T) {
	force := make(chan os.Signal, 1)
	force <- syscall.SIGTERM

	start := time.Now()
	sig := awaitDrainAnnouncement(30*time.Second, force)
	assert.Equal(t, syscall.SIGTERM, sig)
	assert.Less(t, time.Since(start), 5*time.Second,
		"a second signal must not wait out the announce window")
}

func TestAwaitDrainAnnouncement_ZeroWindowReturnsImmediately(t *testing.T) {
	start := time.Now()
	sig := awaitDrainAnnouncement(0, make(chan os.Signal))
	assert.Nil(t, sig)
	assert.Less(t, time.Since(start), time.Second)
}

func readRouterContractFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read router contract file %s", path)
	return string(body)
}

func routerDuration(t *testing.T, body string, pattern *regexp.Regexp) time.Duration {
	t.Helper()
	match := pattern.FindStringSubmatch(body)
	require.Lenf(t, match, 2, "router contract does not match %s", pattern)
	duration, err := time.ParseDuration(match[1])
	require.NoError(t, err)
	return duration
}

func routerInteger(t *testing.T, body string, pattern *regexp.Regexp) int {
	t.Helper()
	match := pattern.FindStringSubmatch(body)
	require.Lenf(t, match, 2, "router contract does not match %s", pattern)
	value, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return value
}
