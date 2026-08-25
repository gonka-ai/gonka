package mode

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDevshardHAHeader(t *testing.T) {
	ha, err := ParseDevshardHAHeader(nil)
	require.NoError(t, err)
	require.False(t, ha)
	ha, err = ParseDevshardHAHeader(http.Header{})
	require.NoError(t, err)
	require.False(t, ha)

	h := http.Header{}
	h.Set(HeaderDevshardHA, "true")
	ha, err = ParseDevshardHAHeader(h)
	require.NoError(t, err)
	require.True(t, ha)

	h.Set(HeaderDevshardHA, "TRUE")
	ha, err = ParseDevshardHAHeader(h)
	require.NoError(t, err)
	require.True(t, ha)

	h.Set(HeaderDevshardHA, "1")
	ha, err = ParseDevshardHAHeader(h)
	require.NoError(t, err)
	require.True(t, ha)

	h.Set(HeaderDevshardHA, "yes")
	ha, err = ParseDevshardHAHeader(h)
	require.NoError(t, err)
	require.True(t, ha)

	h.Set(HeaderDevshardHA, "")
	ha, err = ParseDevshardHAHeader(h)
	require.NoError(t, err)
	require.True(t, ha, "present empty value counts as HA mark")

	h.Set(HeaderDevshardHA, "false")
	ha, err = ParseDevshardHAHeader(h)
	require.NoError(t, err)
	require.False(t, ha)

	h.Set(HeaderDevshardHA, "typo")
	_, err = ParseDevshardHAHeader(h)
	require.Error(t, err)

	h[HeaderDevshardHA] = []string{"true", "false"}
	_, err = ParseDevshardHAHeader(h)
	require.Error(t, err)
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
