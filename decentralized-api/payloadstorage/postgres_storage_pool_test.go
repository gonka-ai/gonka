package payloadstorage

import (
	"context"
	"testing"

	"common/storage/pgpool"

	"github.com/stretchr/testify/require"
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

	_, err := NewPostgresStorage(context.Background())
	require.ErrorContains(t, err, "PG_POOL_MAX_CONNS")
}

func TestNewPostgresStorage_CapsPoolMaxConns(t *testing.T) {
	cleanup, err := setupPostgresContainer(t)
	require.NoError(t, err)
	defer cleanup()

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

			storage, err := NewPostgresStorage(context.Background())
			require.NoError(t, err)
			t.Cleanup(storage.Close)

			require.Equal(t, tt.want, storage.pool.Config().MaxConns)
		})
	}
}
