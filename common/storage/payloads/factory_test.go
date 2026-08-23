package payloads

import (
	"context"
	"path/filepath"
	"testing"

	"common/storage/mode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearStorageModeEnv(t *testing.T) {
	t.Helper()
	t.Setenv(mode.EnvStorageMode, "")
}

func TestOpen_FileFallback_SingleInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "payloads")
	clearStorageModeEnv(t)
	t.Setenv("PGHOST", "")
	t.Setenv("VERSIOND_FORCE", "v1,v2") // multi force alone must not enable postgres mode

	store, closeFn, err := Open(context.Background(), OpenConfig{Dir: dir})
	require.NoError(t, err)
	defer closeFn()

	_, ok := store.(*FileStorage)
	require.True(t, ok, "expected file storage when PGHOST unset and mode auto/sqlite")

	ctx := context.Background()
	prompt := []byte(`{"prompt":true}`)
	response := []byte(`{"response":true}`)
	require.NoError(t, store.Store(ctx, "escrow-1", 1, 10, prompt, response))

	gotPrompt, gotResponse, err := store.Retrieve(ctx, "escrow-1", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, prompt, gotPrompt)
	assert.Equal(t, response, gotResponse)
}

func TestOpen_RequiresPostgres_ExplicitMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "payloads")
	clearStorageModeEnv(t)
	t.Setenv("PGHOST", "")
	t.Setenv("VERSIOND_FORCE", "v2")
	t.Setenv(mode.EnvStorageMode, "postgres")

	_, _, err := Open(context.Background(), OpenConfig{Dir: dir})
	require.ErrorIs(t, err, ErrSharedPostgresRequired)
}

func TestOpen_Postgres_FailsWhenPostgresUnreachable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "payloads")
	clearStorageModeEnv(t)
	t.Setenv(mode.EnvStorageMode, "postgres")
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	_, _, err := Open(context.Background(), OpenConfig{Dir: dir})
	require.Error(t, err)
	require.Contains(t, err.Error(), "postgres mode requires postgres")
}

func TestOpen_Hybrid_RequiresPGHOST(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "payloads")
	clearStorageModeEnv(t)
	t.Setenv(mode.EnvStorageMode, "hybrid")
	t.Setenv("PGHOST", "")

	_, _, err := Open(context.Background(), OpenConfig{Dir: dir})
	require.ErrorIs(t, err, ErrSharedPostgresRequired)
}

func TestOpen_SQLite_IgnoresPGHOST(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "payloads")
	clearStorageModeEnv(t)
	t.Setenv(mode.EnvStorageMode, "sqlite")
	t.Setenv("PGHOST", "127.0.0.1")

	store, closeFn, err := Open(context.Background(), OpenConfig{Dir: dir})
	require.NoError(t, err)
	defer closeFn()
	_, ok := store.(*FileStorage)
	require.True(t, ok, "explicit sqlite must stay file-only even when PGHOST is set")
}

func TestFileStorage_DropEpoch(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)
	ctx := context.Background()

	require.NoError(t, store.Store(ctx, "escrow-1", 1, 9, []byte("a"), []byte("b")))
	require.NoError(t, store.Store(ctx, "escrow-1", 2, 10, []byte("c"), []byte("d")))

	require.NoError(t, store.DropEpoch(ctx, 10))

	_, _, err := store.Retrieve(ctx, "escrow-1", 1, 9)
	require.NoError(t, err)
	_, _, err = store.Retrieve(ctx, "escrow-1", 2, 10)
	require.ErrorIs(t, err, ErrNotFound)
}
