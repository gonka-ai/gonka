package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPGMigrationTimeout(t *testing.T) {
	t.Setenv("PG_MIGRATION_TIMEOUT", "45s")
	require.Equal(t, 45*time.Second, pgMigrationTimeout())

	t.Setenv("PG_MIGRATION_TIMEOUT", "invalid")
	require.Equal(t, defaultPGMigrationTimeout, pgMigrationTimeout())

	t.Setenv("PG_MIGRATION_TIMEOUT", "0")
	require.Equal(t, defaultPGMigrationTimeout, pgMigrationTimeout())
}
