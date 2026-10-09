package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveRuntimeModel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "chain/Model", resolveRuntimeModel("local/Model", "chain/Model", "default/Model", "12"))
	require.Equal(t, "chain/Model", resolveRuntimeModel("", "chain/Model", "default/Model", "12"))
	require.Equal(t, "local/Model", resolveRuntimeModel("local/Model", "", "default/Model", "12"))
	require.Equal(t, "default/Model", resolveRuntimeModel("", "", "default/Model", "12"))
	require.Equal(t, "chain/Model", resolveRuntimeModel("  local/Model  ", "  chain/Model  ", "default/Model", "12"))
}

func TestPersistRuntimeModelUpdatesStore(t *testing.T) {
	store, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	require.NoError(t, store.Initialize(context.Background(), GatewaySettings{DefaultModel: "Qwen/Default"}, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "42", PrivateKeyHex: "secret", Model: "Qwen/Default"},
		Active:        true,
	}}))

	state, ok, err := store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	persistRuntimeModel(context.Background(), store, &state, "42", "moonshotai/Kimi-K2.6")

	record, found, err := store.GetDevshard(context.Background(), "42")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "moonshotai/Kimi-K2.6", record.Model)
	require.Equal(t, "moonshotai/Kimi-K2.6", state.Devshards[0].Model)
}
