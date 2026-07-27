package main

import (
	"testing"

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
