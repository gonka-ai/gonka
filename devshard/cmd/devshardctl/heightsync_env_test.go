package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func resetHeightSyncForTest() {
	hsOnce = sync.Once{}
	hsSt = nil
	hsErr = nil
}

func TestExtraClientConfigFromEnv_EmptyIsNil(t *testing.T) {
	resetHeightSyncForTest()
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "")
	t.Setenv("DEVSHARD_HEIGHTSYNC", "")
	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.Nil(t, cfg)
}

func TestExtraClientConfigFromEnv_InvalidK(t *testing.T) {
	resetHeightSyncForTest()
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "http://127.0.0.1:9")
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "xyz")
	cfg, err := extraClientConfigFromEnv()
	require.Error(t, err)
	require.Nil(t, cfg)
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

func TestHeightSyncFlagUsesDevshardBooleanGrammar(t *testing.T) {
	t.Setenv(envHeightSync, "ON")
	require.True(t, heightSyncFlag())

	t.Setenv(envHeightSync, "no")
	require.False(t, heightSyncFlag())

	t.Setenv(envHeightSync, "invalid")
	require.False(t, heightSyncFlag(), "invalid values retain the disabled fallback")
}
