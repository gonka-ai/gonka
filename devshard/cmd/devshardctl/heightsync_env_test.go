package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func resetHeightSyncForTest() {
	if hsSt != nil && hsSt.closer != nil {
		hsSt.closer()
	}
	hsOnce = sync.Once{}
	hsSt = nil
	hsErr = nil
}

func unsetHeightSyncSources(t *testing.T) {
	t.Helper()
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "")
	t.Setenv("DEVSHARD_CHAIN_RPC", "")
	t.Setenv("NODE_RPC_URL", "")
	t.Setenv("DEVSHARD_COMET_RPC", "")
}

func TestExtraClientConfigFromEnv_EmptyIsNil(t *testing.T) {
	resetHeightSyncForTest()
	unsetHeightSyncSources(t)
	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.Nil(t, cfg)
}

func TestExtraClientConfigFromEnv_InvalidK(t *testing.T) {
	resetHeightSyncForTest()
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "http://127.0.0.1:9")
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "xyz")
	cfg, err := extraClientConfigFromEnv()
	require.Error(t, err)
	require.Nil(t, cfg)
}

func TestExtraClientConfigFromEnv_ChainRPCEnablesWithoutOracleURL(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("NODE_RPC_URL", "http://127.0.0.1:26657")
	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.HeightSync)
}

func TestParseUintEnv(t *testing.T) {
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "")
	v, err := parseUintEnv("DEVSHARD_HEIGHTSYNC_K")
	require.NoError(t, err)
	require.Equal(t, uint64(0), v)

	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "10")
	v, err = parseUintEnv("DEVSHARD_HEIGHTSYNC_K")
	require.NoError(t, err)
	require.Equal(t, uint64(10), v)
}
