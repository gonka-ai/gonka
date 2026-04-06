package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupStorage(t *testing.T) *Storage {
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
	s, err := New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(s.Close)

	return s
}

// --- Payloads ---

func TestStorage_StoreAndRetrievePayload(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	prompt := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	response := []byte(`{"choices":[{"message":{"content":"world"}}]}`)

	require.NoError(t, s.StorePayload(ctx, "inf-001", 10, prompt, response))

	gotPrompt, gotResponse, err := s.RetrievePayload(ctx, "inf-001", 10)
	require.NoError(t, err)
	assert.Equal(t, prompt, gotPrompt)
	assert.Equal(t, response, gotResponse)
}

func TestStorage_RetrievePayload_NotFound(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	_, _, err := s.RetrievePayload(ctx, "nonexistent", 10)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestStorage_StorePayload_Idempotent(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	first := []byte(`{"first": true}`)
	require.NoError(t, s.StorePayload(ctx, "inf-001", 10, first, []byte(`{}`)))
	// Second store with same ID is a no-op — first value is kept.
	require.NoError(t, s.StorePayload(ctx, "inf-001", 10, []byte(`{"second": true}`), []byte(`{}`)))

	got, _, err := s.RetrievePayload(ctx, "inf-001", 10)
	require.NoError(t, err)
	assert.Equal(t, first, got)
}

func TestStorage_StorePayload_MultipleEpochs(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	for _, epoch := range []uint64{10, 11, 12} {
		require.NoError(t, s.StorePayload(ctx, "inf-001", epoch, []byte(`{}`), []byte(`{}`)))
	}
	for _, epoch := range []uint64{10, 11, 12} {
		_, _, err := s.RetrievePayload(ctx, "inf-001", epoch)
		require.NoError(t, err, "epoch %d", epoch)
	}
}

func TestStorage_PrunePayloads(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	require.NoError(t, s.StorePayload(ctx, "inf-001", 10, []byte(`{}`), []byte(`{}`)))
	require.NoError(t, s.PrunePayloads(ctx, 10))

	_, _, err := s.RetrievePayload(ctx, "inf-001", 10)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestStorage_PrunePayloads_NonExistentEpoch(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	assert.NoError(t, s.PrunePayloads(ctx, 999))
}

// --- Stats ---

func TestStorage_UpsertStats(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	stats := InferenceRecord{
		InferenceID:          "inf-001",
		EpochID:              10,
		RequestedBy:          "dev-addr",
		Model:                "model-a",
		PromptTokenCount:     100,
		CompletionTokenCount: 200,
		TotalTokenCount:      300,
		InferenceTimestamp:   UnixMillis(time.Now().UTC().UnixMilli()),
	}
	require.NoError(t, s.UpsertStats(ctx, stats))

	// Upsert again with updated token counts — should not error.
	stats.PromptTokenCount = 150
	stats.TotalTokenCount = 350
	require.NoError(t, s.UpsertStats(ctx, stats))
}

// --- Claims ---

func TestStorage_Claim_FirstWins(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	won, err := s.Claim(ctx, "inf-001", 10, "instance-1")
	require.NoError(t, err)
	assert.True(t, won)

	// Second claim for same inference — loses.
	won, err = s.Claim(ctx, "inf-001", 10, "instance-2")
	require.NoError(t, err)
	assert.False(t, won)
}

func TestStorage_Claim_DifferentInferences(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	won1, err := s.Claim(ctx, "inf-001", 10, "instance-1")
	require.NoError(t, err)
	assert.True(t, won1)

	won2, err := s.Claim(ctx, "inf-002", 10, "instance-1")
	require.NoError(t, err)
	assert.True(t, won2)
}

func TestStorage_ReclaimOneStale_NoStale(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	// Claim is fresh — should not be reclaimed.
	_, err := s.Claim(ctx, "inf-001", 10, "instance-1")
	require.NoError(t, err)

	id, err := s.ReclaimOneStale(ctx, "instance-2", "30 minutes")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestStorage_ReclaimOneStale_PicksStale(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	// Insert a claim and backdate it to make it stale.
	_, err := s.Claim(ctx, "inf-001", 10, "instance-1")
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx,
		`UPDATE validation_claims SET claimed_at = now() - interval '1 hour' WHERE inference_id = 'inf-001'`,
	)
	require.NoError(t, err)

	id, err := s.ReclaimOneStale(ctx, "instance-2", "30 minutes")
	require.NoError(t, err)
	assert.Equal(t, "inf-001", id)
}

func TestStorage_ReclaimOneStale_SkipsCompleted(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	// Claim, backdate, then mark as completed with a tx_hash.
	_, err := s.Claim(ctx, "inf-001", 10, "instance-1")
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx,
		`UPDATE validation_claims SET claimed_at = now() - interval '1 hour', tx_hash = 'txhash123' WHERE inference_id = 'inf-001'`,
	)
	require.NoError(t, err)

	id, err := s.ReclaimOneStale(ctx, "instance-2", "30 minutes")
	require.NoError(t, err)
	assert.Empty(t, id, "completed claims should not be reclaimed")
}

func TestStorage_SetTxHash(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, "inf-001", 10, "instance-1")
	require.NoError(t, err)

	require.NoError(t, s.SetTxHash(ctx, "inf-001", "0xdeadbeef"))

	// After tx_hash is set, stale reclaim should skip it.
	_, err = s.pool.Exec(ctx,
		`UPDATE validation_claims SET claimed_at = now() - interval '1 hour' WHERE inference_id = 'inf-001'`,
	)
	require.NoError(t, err)

	id, err := s.ReclaimOneStale(ctx, "instance-2", "30 minutes")
	require.NoError(t, err)
	assert.Empty(t, id)
}

// --- Error paths (closed pool) ---

func TestStorage_New_BadDSN(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping storage tests in -short mode (requires Docker)")
	}
	_, err := New(context.Background(), "postgres://bad:bad@localhost:1/nodb?connect_timeout=1")
	require.Error(t, err)
}

func TestStorage_ErrorPaths(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	// Seed some data while the pool is open.
	require.NoError(t, s.StorePayload(ctx, "inf-001", 10, []byte(`{}`), []byte(`{}`)))
	_, err := s.Claim(ctx, "inf-001", 10, "instance-1")
	require.NoError(t, err)

	// Close the pool — all subsequent operations should error.
	s.Close()

	assert.Error(t, s.StorePayload(ctx, "inf-002", 10, []byte(`{}`), []byte(`{}`)))
	_, _, err = s.RetrievePayload(ctx, "inf-001", 10)
	assert.Error(t, err)
	assert.Error(t, s.PrunePayloads(ctx, 10))
	assert.Error(t, s.UpsertStats(ctx, InferenceRecord{InferenceID: "inf-001", EpochID: 10, RequestedBy: "d", Model: "m"}))
	_, err = s.Claim(ctx, "inf-002", 10, "instance-1")
	assert.Error(t, err)
	_, err = s.ReclaimOneStale(ctx, "instance-1", "30 minutes")
	assert.Error(t, err)
	assert.Error(t, s.SetTxHash(ctx, "inf-001", "0xabc"))
	assert.Error(t, s.DeleteClaimsByEpoch(ctx, 10))
}

func TestStorage_DeleteClaimsByEpoch(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, "inf-001", 5, "instance-1")
	require.NoError(t, err)
	_, err = s.Claim(ctx, "inf-002", 10, "instance-1")
	require.NoError(t, err)

	// Delete epochs < 10 — removes epoch 5, keeps epoch 10.
	require.NoError(t, s.DeleteClaimsByEpoch(ctx, 10))

	var count int
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM validation_claims`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
