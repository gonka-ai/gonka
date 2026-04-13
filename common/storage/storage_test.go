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

// --- Error paths (closed pool) ---

func TestStorage_New_BadDSN(t *testing.T) {
	_, err := New(context.Background(), "postgres://bad:bad@localhost:1/nodb?connect_timeout=1")
	require.Error(t, err)
}

func TestStorage_UpsertStats_ClosedPool(t *testing.T) {
	s := setupStorage(t)
	ctx := context.Background()

	// Close the pool — all subsequent operations should error.
	s.Close()

	assert.Error(t, s.UpsertStats(ctx, InferenceRecord{InferenceID: "inf-001", EpochID: 10, RequestedBy: "d", Model: "m"}))
}
