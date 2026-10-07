package main

import (
	"path/filepath"
	"testing"
	"time"

	"devshard/host"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

func TestCompletionEscrowCleanupDeletesPayloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := NewGatewayStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Initialize(GatewaySettings{DefaultModel: "llama"}.WithTuningDefaults(), []GatewayDevshardState{{RuntimeConfig: RuntimeConfig{ID: "42", Model: "llama"}}, {RuntimeConfig: RuntimeConfig{ID: "43", Model: "llama"}}}))
	entry := user.InferenceCompletion{Nonce: 1, PreparedAt: time.Now().UnixNano(), Payload: host.InferencePayload{Prompt: []byte(`{"messages":[{"role":"user","content":"sensitive user prompt"}]}`), Model: "llama"}}
	require.NoError(t, store.SaveInferenceCompletion("42", entry))
	require.NoError(t, store.SaveInferenceCompletion("43", entry))
	exists, err := store.HasInferenceCompletion("42", 1)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = store.HasInferenceCompletion("42", 2)
	require.NoError(t, err)
	require.False(t, exists)
	require.NoError(t, store.DeleteDevshard(" 42 "))
	require.NoError(t, store.Close())
	store, err = NewGatewayStore(path)
	require.NoError(t, err)
	defer store.Close()
	entries, err := store.ListInferenceCompletions("42")
	require.NoError(t, err)
	require.Empty(t, entries)
	entries, err = store.ListInferenceCompletions("43")
	require.NoError(t, err)
	require.Equal(t, []user.InferenceCompletion{entry}, entries)
}

func TestCompletionEscrowCleanupRollsBackOnPayloadFailure(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.Initialize(GatewaySettings{DefaultModel: "llama"}.WithTuningDefaults(), []GatewayDevshardState{{RuntimeConfig: RuntimeConfig{ID: "42", Model: "llama"}}}))
	entry := user.InferenceCompletion{Nonce: 1, PreparedAt: time.Now().UnixNano()}
	require.NoError(t, store.SaveInferenceCompletion("42", entry))
	_, err = store.db.Exec(`CREATE TRIGGER fail_completion_delete BEFORE DELETE ON gateway_inference_completions BEGIN SELECT RAISE(ABORT, 'temporary payload deletion failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, store.DeleteDevshard("42"), "temporary payload deletion failure")
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, state.Devshards, 1)
	entries, err := store.ListInferenceCompletions("42")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	_, err = store.db.Exec(`DROP TRIGGER fail_completion_delete`)
	require.NoError(t, err)
	require.NoError(t, store.DeleteDevshard("42"))
}
