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

// GONKA_HA gates a safety guard, so its grammar is closed on both ends: every
// spelling of "off" is off, and a value outside the grammar refuses to boot.
// The old parser read a typo as "off" — the one value that silently disables
// the guard the variable exists to enable.
func TestHADeploymentGrammar(t *testing.T) {
	t.Setenv(EnvStorageMode, "sqlite")

	for _, off := range []string{"", "0", "false", "no", "FALSE", " no "} {
		t.Setenv(EnvHADeployment, off)
		require.NoError(t, RequireHADeploymentStorage(),
			"%q must read as off, and off must not require postgres", off)
	}

	for _, on := range []string{"1", "true", "yes", "TRUE", "Yes"} {
		t.Setenv(EnvHADeployment, on)
		require.Error(t, RequireHADeploymentStorage(),
			"%q must read as on, and on must refuse sqlite", on)
	}

	for _, garbage := range []string{"maybe", "tru", "on", "2"} {
		t.Setenv(EnvHADeployment, garbage)
		err := RequireHADeploymentStorage()
		require.Error(t, err, "%q is outside the grammar and must refuse to boot", garbage)
		require.Contains(t, err.Error(), "not a boolean",
			"the refusal for %q must name the actual problem", garbage)
	}
}
