package mode

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func clearModeEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvStorageMode, "")
	t.Setenv("PGHOST", "")
	t.Setenv("VERSIOND_FORCE", "")
}

func TestResolve_DefaultAutoIsSQLite(t *testing.T) {
	clearModeEnv(t)
	t.Setenv("VERSIOND_FORCE", "v1,v2")

	m, err := Resolve()
	require.NoError(t, err)
	require.Equal(t, SQLite, m, "VERSIOND_FORCE alone must not select postgres")
}

func TestResolve_AutoHybridWhenPGHOST(t *testing.T) {
	clearModeEnv(t)
	t.Setenv("PGHOST", "db.example")

	m, err := Resolve()
	require.NoError(t, err)
	require.Equal(t, Hybrid, m)
}

func TestResolve_ExplicitOverridesPGHOST(t *testing.T) {
	clearModeEnv(t)
	t.Setenv("PGHOST", "db.example")
	t.Setenv(EnvStorageMode, "sqlite")

	m, err := Resolve()
	require.NoError(t, err)
	require.Equal(t, SQLite, m)
}

func TestResolve_ExplicitModes(t *testing.T) {
	clearModeEnv(t)
	for _, want := range []Mode{SQLite, Hybrid, Postgres, Auto} {
		t.Setenv(EnvStorageMode, string(want))
		m, err := Resolve()
		require.NoError(t, err)
		if want == Auto {
			require.Equal(t, SQLite, m)
			continue
		}
		require.Equal(t, want, m)
	}
}

func TestResolve_Invalid(t *testing.T) {
	clearModeEnv(t)
	t.Setenv(EnvStorageMode, "redis")

	_, err := Resolve()
	require.Error(t, err)
	require.Contains(t, err.Error(), EnvStorageMode)
}

func TestModeHelpers(t *testing.T) {
	require.False(t, SQLite.RequiresPGHOST())
	require.True(t, Hybrid.RequiresPGHOST())
	require.True(t, Postgres.RequiresPGHOST())

	require.False(t, SQLite.FailClosed())
	require.False(t, Hybrid.FailClosed())
	require.True(t, Postgres.FailClosed())
}
