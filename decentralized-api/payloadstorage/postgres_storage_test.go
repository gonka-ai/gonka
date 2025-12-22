package payloadstorage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupPostgresContainer(t *testing.T) (func(), error) {
	ctx := context.Background()

	container, err := postgres.Run(ctx,
		"postgres:18.1-bookworm",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, err
	}

	host, err := container.Host(ctx)
	if err != nil {
		container.Terminate(ctx)
		return nil, err
	}

	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		container.Terminate(ctx)
		return nil, err
	}

	// Set environment variables for pgx
	os.Setenv("PGHOST", host)
	os.Setenv("PGPORT", port.Port())
	os.Setenv("PGDATABASE", "testdb")
	os.Setenv("PGUSER", "testuser")
	os.Setenv("PGPASSWORD", "testpass")

	cleanup := func() {
		os.Unsetenv("PGHOST")
		os.Unsetenv("PGPORT")
		os.Unsetenv("PGDATABASE")
		os.Unsetenv("PGUSER")
		os.Unsetenv("PGPASSWORD")
		container.Terminate(ctx)
	}

	return cleanup, nil
}

func TestPostgresStorage_StoreAndRetrieve(t *testing.T) {
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

	ctx := context.Background()
	storage, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	defer storage.Close()

	// Test store
	storedPrompt := []byte(`{"prompt": "hello"}`)
	storedResponse := []byte(`{"response": "world"}`)
	err = storage.Store(ctx, "inf-001", 100, storedPrompt, storedResponse)
	require.NoError(t, err)

	// Test retrieve
	prompt, response, err := storage.Retrieve(ctx, "inf-001", 100)
	require.NoError(t, err)
	assert.Equal(t, storedPrompt, prompt)
	assert.Equal(t, storedResponse, response)

	// Test not found
	_, _, err = storage.Retrieve(ctx, "nonexistent", 100)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPostgresStorage_PartitionAutoCreation(t *testing.T) {
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

	ctx := context.Background()
	storage, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	defer storage.Close()

	// Store in multiple epochs - partitions should be created automatically
	epochs := []uint64{100, 101, 102}
	for _, epoch := range epochs {
		err := storage.Store(ctx, "inf-001", epoch, []byte(`{"epoch": "`+string(rune(epoch))+`"}`), []byte(`{"resp": "ok"}`))
		require.NoError(t, err, "Failed to store in epoch %d", epoch)
	}

	// Verify all can be retrieved
	for _, epoch := range epochs {
		_, _, err := storage.Retrieve(ctx, "inf-001", epoch)
		require.NoError(t, err, "Failed to retrieve from epoch %d", epoch)
	}
}

func TestPostgresStorage_PruneEpoch(t *testing.T) {
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

	ctx := context.Background()
	storage, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	defer storage.Close()

	// Store data in epoch 100
	err = storage.Store(ctx, "inf-001", 100, []byte(`{"prompt": "test"}`), []byte(`{"response": "test"}`))
	require.NoError(t, err)

	// Verify it exists
	_, _, err = storage.Retrieve(ctx, "inf-001", 100)
	require.NoError(t, err)

	// Prune epoch 100
	err = storage.PruneEpoch(ctx, 100)
	require.NoError(t, err)

	// Verify it's gone
	_, _, err = storage.Retrieve(ctx, "inf-001", 100)
	assert.ErrorIs(t, err, ErrNotFound)

	// Prune non-existent epoch should not error
	err = storage.PruneEpoch(ctx, 999)
	require.NoError(t, err)
}

func TestPostgresStorage_SchemaAutoCreation(t *testing.T) {
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

	ctx := context.Background()

	// First connection creates schema
	storage1, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	storage1.Close()

	// Second connection should work with existing schema
	storage2, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	defer storage2.Close()

	// Should be able to store and retrieve
	storedPrompt := []byte(`{"test": "data"}`)
	storedResponse := []byte(`{"response": "ok"}`)
	err = storage2.Store(ctx, "inf-001", 100, storedPrompt, storedResponse)
	require.NoError(t, err)

	prompt, _, err := storage2.Retrieve(ctx, "inf-001", 100)
	require.NoError(t, err)
	assert.Equal(t, storedPrompt, prompt)
}

func TestPostgresStorage_IdempotentStore(t *testing.T) {
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

	ctx := context.Background()
	storage, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	defer storage.Close()

	// First store
	storedFirstValue := []byte(`{"first": "value"}`)
	err = storage.Store(ctx, "inf-001", 100, storedFirstValue, []byte(`{"response": "first"}`))
	require.NoError(t, err)

	// Second store with same ID should not error (ON CONFLICT DO NOTHING)
	err = storage.Store(ctx, "inf-001", 100, []byte(`{"second": "value"}`), []byte(`{"response": "second"}`))
	require.NoError(t, err)

	// Should still have first value
	prompt, _, err := storage.Retrieve(ctx, "inf-001", 100)
	require.NoError(t, err)
	assert.Equal(t, storedFirstValue, prompt)
}

func TestHybridStorage_FallbackOnPGError(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	// Create file storage only (no PG)
	fileStorage := NewFileStorage(tempDir)

	// Store something in file storage
	storedPrompt := []byte(`{"file": "data"}`)
	storedResponse := []byte(`{"file": "response"}`)
	err := fileStorage.Store(ctx, "inf-001", 100, storedPrompt, storedResponse)
	require.NoError(t, err)

	// Now create hybrid with a broken PG connection
	// Since PGHOST is not set, NewPostgresStorage will fail
	// But we test the Retrieve fallback manually

	// Start postgres container
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

	pgStorage, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	defer pgStorage.Close()

	hybrid := NewHybridStorage(pgStorage, fileStorage, 240*time.Second)

	// Data not in PG, but is in file - should find it
	prompt, response, err := hybrid.Retrieve(ctx, "inf-001", 100)
	require.NoError(t, err)
	assert.Equal(t, storedPrompt, prompt)
	assert.Equal(t, storedResponse, response)
}

func TestHybridStorage_PGPrimary(t *testing.T) {
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

	ctx := context.Background()
	tempDir := t.TempDir()

	pgStorage, err := NewPostgresStorage(ctx)
	require.NoError(t, err)
	defer pgStorage.Close()

	fileStorage := NewFileStorage(tempDir)
	hybrid := NewHybridStorage(pgStorage, fileStorage, 240*time.Second)

	// Store via hybrid (should go to PG)
	storedPrompt := []byte(`{"pg": "data"}`)
	storedResponse := []byte(`{"pg": "response"}`)
	err = hybrid.Store(ctx, "inf-001", 100, storedPrompt, storedResponse)
	require.NoError(t, err)

	// Retrieve should find it in PG
	prompt, response, err := hybrid.Retrieve(ctx, "inf-001", 100)
	require.NoError(t, err)
	assert.Equal(t, storedPrompt, prompt)
	assert.Equal(t, storedResponse, response)

	// File storage should NOT have it
	_, _, err = fileStorage.Retrieve(ctx, "inf-001", 100)
	assert.ErrorIs(t, err, ErrNotFound)
}
