package payloads_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"common/storage/payloads"
)

func setupStore(t *testing.T) *payloads.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping storage tests in -short mode (requires Docker)")
	}
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
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://testuser:testpass@%s:%s/testdb", host, port.Port())
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	store, err := payloads.New(ctx, pool)
	require.NoError(t, err)
	return store
}

// TestStore_StoreAndRetrieve stores a payload and retrieves it, asserting prompt and response match.
func TestStore_StoreAndRetrieve(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	prompt := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	response := []byte(`{"choices":[{"message":{"content":"world"}}]}`)

	require.NoError(t, store.Store(ctx, "escrow-1", 1, 10, prompt, response))

	gotPrompt, gotResponse, err := store.Retrieve(ctx, "escrow-1", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, prompt, gotPrompt)
	assert.Equal(t, response, gotResponse)
}

// TestStore_Retrieve_NotFound retrieves a non-existent key and asserts ErrNotFound.
func TestStore_Retrieve_NotFound(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	_, _, err := store.Retrieve(ctx, "nonexistent", 1, 10)
	assert.ErrorIs(t, err, payloads.ErrNotFound)
}

// TestStore_Store_Idempotent stores with same key twice with different data,
// then retrieves and asserts the first data is kept (ON CONFLICT DO NOTHING).
func TestStore_Store_Idempotent(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	first := []byte(`{"first": true}`)
	second := []byte(`{"second": true}`)

	require.NoError(t, store.Store(ctx, "escrow-1", 1, 10, first, []byte(`{}`)))
	// Second store with same key is a no-op — first value is kept.
	require.NoError(t, store.Store(ctx, "escrow-1", 1, 10, second, []byte(`{}`)))

	got, _, err := store.Retrieve(ctx, "escrow-1", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, first, got)
}

// TestStore_Store_MultipleEscrows stores (escrow-1, inf-001, epoch 10) and (escrow-2, inf-001, epoch 10)
// with different prompt data and retrieves each, asserting they return their respective data independently.
func TestStore_Store_MultipleEscrows(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	prompt1 := []byte(`{"escrow": 1}`)
	prompt2 := []byte(`{"escrow": 2}`)

	require.NoError(t, store.Store(ctx, "escrow-1", 1, 10, prompt1, []byte(`{}`)))
	require.NoError(t, store.Store(ctx, "escrow-2", 1, 10, prompt2, []byte(`{}`)))

	got1, _, err := store.Retrieve(ctx, "escrow-1", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, prompt1, got1)

	got2, _, err := store.Retrieve(ctx, "escrow-2", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, prompt2, got2)
}

// TestStore_Store_MultipleEpochs stores (escrow-1, inf-001) for epochs 10, 11, 12,
// retrieves each, and asserts the stored data is returned correctly.
func TestStore_Store_MultipleEpochs(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	for _, epoch := range []uint64{10, 11, 12} {
		prompt := fmt.Appendf(nil, `{"epoch":%d}`, epoch)
		require.NoError(t, store.Store(ctx, "escrow-1", 1, epoch, prompt, []byte(`{}`)))
	}

	for _, epoch := range []uint64{10, 11, 12} {
		wantPrompt := fmt.Appendf(nil, `{"epoch":%d}`, epoch)
		gotPrompt, _, err := store.Retrieve(ctx, "escrow-1", 1, epoch)
		require.NoError(t, err, "epoch %d", epoch)
		assert.Equal(t, wantPrompt, gotPrompt, "epoch %d", epoch)
	}
}

// TestStore_PruneEpoch stores epochs 9, 10, and 11, calls PruneEpoch(11),
// asserts epochs 9 and 10 (< 11) return ErrNotFound, epoch 11 still exists.
func TestStore_PruneEpoch(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	require.NoError(t, store.Store(ctx, "escrow-1", 1, 9, []byte(`{}`), []byte(`{}`)))
	require.NoError(t, store.Store(ctx, "escrow-1", 2, 10, []byte(`{}`), []byte(`{}`)))
	require.NoError(t, store.Store(ctx, "escrow-1", 3, 11, []byte(`{}`), []byte(`{}`)))

	// PruneEpoch(11) removes epoch_id < 11 (i.e., epochs 9 and 10), keeps 11.
	require.NoError(t, store.PruneEpoch(ctx, 11))

	_, _, err := store.Retrieve(ctx, "escrow-1", 1, 9)
	assert.ErrorIs(t, err, payloads.ErrNotFound, "epoch 9 should be pruned")

	_, _, err = store.Retrieve(ctx, "escrow-1", 2, 10)
	assert.ErrorIs(t, err, payloads.ErrNotFound, "epoch 10 should be pruned")

	_, _, err = store.Retrieve(ctx, "escrow-1", 3, 11)
	assert.NoError(t, err, "epoch 11 should be retained")
}

// TestStore_PruneEpoch_NonExistent calls PruneEpoch on a non-existent epoch,
// asserts no error.
func TestStore_PruneEpoch_NonExistent(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	assert.NoError(t, store.PruneEpoch(ctx, 999))
}
