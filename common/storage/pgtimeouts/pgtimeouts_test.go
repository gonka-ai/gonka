package pgtimeouts

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestConnectTimeout(t *testing.T) {
	t.Setenv(EnvConnectTimeout, "1500ms")
	require.Equal(t, 1500*time.Millisecond, ConnectTimeout())

	t.Setenv(EnvConnectTimeout, "bad")
	require.Equal(t, DefaultConnectTimeout, ConnectTimeout())

	t.Setenv(EnvConnectTimeout, "0")
	require.Equal(t, DefaultConnectTimeout, ConnectTimeout())

	t.Setenv(EnvConnectTimeout, "")
	require.Equal(t, DefaultConnectTimeout, ConnectTimeout())
}

func TestOperationTimeout(t *testing.T) {
	t.Setenv(EnvOperationTimeout, "1500ms")
	require.Equal(t, 1500*time.Millisecond, OperationTimeout())

	t.Setenv(EnvOperationTimeout, "0")
	require.Equal(t, time.Duration(0), OperationTimeout())

	t.Setenv(EnvOperationTimeout, "bad")
	require.Equal(t, DefaultOperationTimeout, OperationTimeout())

	t.Setenv(EnvOperationTimeout, "")
	require.Equal(t, DefaultOperationTimeout, OperationTimeout())
}

func TestImportTimeout(t *testing.T) {
	t.Setenv(EnvImportTimeout, "90s")
	require.Equal(t, 90*time.Second, ImportTimeout())

	t.Setenv(EnvImportTimeout, "")
	require.Equal(t, DefaultImportTimeout, ImportTimeout())
}

func TestApplyConnConfig(t *testing.T) {
	t.Setenv(EnvConnectTimeout, "1s")
	cfg, err := pgx.ParseConfig("")
	require.NoError(t, err)
	ApplyConnConfig(cfg)
	require.Equal(t, time.Second, cfg.ConnectTimeout)
	require.Equal(t, "5000", cfg.RuntimeParams["statement_timeout"])
	require.Equal(t, "3000", cfg.RuntimeParams["lock_timeout"])
}
