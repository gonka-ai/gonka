package mode

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
