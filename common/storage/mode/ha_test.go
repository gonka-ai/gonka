package mode

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHasDevshardHAHeader(t *testing.T) {
	require.False(t, HasDevshardHAHeader(nil))
	require.False(t, HasDevshardHAHeader(http.Header{}))

	h := http.Header{}
	h.Set(HeaderDevshardHA, "true")
	require.True(t, HasDevshardHAHeader(h))

	h.Set(HeaderDevshardHA, "TRUE")
	require.True(t, HasDevshardHAHeader(h))

	h.Set(HeaderDevshardHA, "1")
	require.True(t, HasDevshardHAHeader(h))

	h.Set(HeaderDevshardHA, "yes")
	require.True(t, HasDevshardHAHeader(h))

	h.Set(HeaderDevshardHA, "")
	require.True(t, HasDevshardHAHeader(h), "present empty value counts as HA mark")

	h.Set(HeaderDevshardHA, "false")
	require.False(t, HasDevshardHAHeader(h))
}

func TestConfiguredForHA(t *testing.T) {
	clearModeEnv(t)
	require.False(t, ConfiguredForHA())
	require.Error(t, RequireConfiguredForHA())

	t.Setenv("PGHOST", "db.example")
	require.False(t, ConfiguredForHA(), "PGHOST alone is not enough")

	t.Setenv(EnvStorageMode, "auto")
	require.False(t, ConfiguredForHA(), "auto must not count as HA")

	t.Setenv(EnvStorageMode, "hybrid")
	require.False(t, ConfiguredForHA(), "hybrid must not count as HA")

	t.Setenv(EnvStorageMode, "sqlite")
	require.False(t, ConfiguredForHA())

	t.Setenv(EnvStorageMode, "postgres")
	require.True(t, ConfiguredForHA())
	require.NoError(t, RequireConfiguredForHA())

	t.Setenv("PGHOST", "")
	require.False(t, ConfiguredForHA())
	err := RequireConfiguredForHA()
	require.Error(t, err)
	require.Contains(t, err.Error(), "PGHOST")
}

func TestRequireHADeploymentStorage(t *testing.T) {
	t.Setenv(EnvHADeployment, "")
	t.Setenv(EnvStorageMode, "sqlite")
	require.NoError(t, RequireHADeploymentStorage(),
		"single-instance deployment must not require postgres")

	t.Setenv(EnvHADeployment, "true")
	require.Error(t, RequireHADeploymentStorage(),
		"HA deployment on sqlite must refuse to start")

	t.Setenv(EnvStorageMode, "hybrid")
	t.Setenv("PGHOST", "pg")
	require.Error(t, RequireHADeploymentStorage(),
		"hybrid keeps a local fallback and is not fail-closed")

	t.Setenv(EnvStorageMode, "postgres")
	require.NoError(t, RequireHADeploymentStorage())

	t.Setenv("PGHOST", "")
	require.Error(t, RequireHADeploymentStorage(), "postgres without PGHOST")
}
