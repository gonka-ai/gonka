package payloads

import (
	"context"
	"testing"
	"time"

	"common/storage/pgpool"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The pool cap is applied before any connection is made, so an invalid
// PG_POOL_MAX_CONNS must fail the constructor with the helper's error even when
// no Postgres is reachable. Without the helper the constructor would get as far
// as the ping and fail on the connection instead.
func TestNewPostgresStorage_RejectsInvalidPoolCap(t *testing.T) {
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGCONNECT_TIMEOUT", "1")
	t.Setenv("PG_POOL_MAX_CONNS", "0")

	_, err := newPostgresStorage(context.Background())
	require.ErrorContains(t, err, "PG_POOL_MAX_CONNS")
}

func TestNewPostgresStorage_CapsPoolMaxConns(t *testing.T) {
	startPostgresForPoolTest(t)

	tests := []struct {
		name string
		env  string
		want int32
	}{
		{name: "default", env: "", want: pgpool.DefaultMaxConns},
		// 3 is below the floor pgx applies to an uncapped pool, so no CPU count yields it by accident.
		{name: "from env", env: "3", want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PG_POOL_MAX_CONNS", tt.env)

			store, err := newPostgresStorage(context.Background())
			require.NoError(t, err)
			t.Cleanup(store.Close)

			require.Equal(t, tt.want, store.pool.Config().MaxConns)
		})
	}
}

// startPostgresForPoolTest starts a container and points the libpq env vars at
// it, since newPostgresStorage reads its connection settings from the environment.
func startPostgresForPoolTest(t *testing.T) {
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

	t.Setenv("PGHOST", host)
	t.Setenv("PGPORT", port.Port())
	t.Setenv("PGDATABASE", "testdb")
	t.Setenv("PGUSER", "testuser")
	t.Setenv("PGPASSWORD", "testpass")
}
