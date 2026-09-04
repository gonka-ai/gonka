package harness

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Step logs a citest milestone when verbose (default: always in integration tests).
func Step(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf("citest: "+format, args...)
}

// DumpComposeLogs prints recent compose logs on failure.
func DumpComposeLogs(t *testing.T, s *Stack, services ...string) {
	t.Helper()
	if s == nil || s.ComposePath == "" {
		return
	}
	if len(services) == 0 {
		services = []string{}
	}
	out, err := s.ComposeLogs(services...)
	if err != nil {
		t.Logf("citest: compose logs: %v", err)
		return
	}
	if len(out) == 0 {
		return
	}
	t.Logf("citest: compose logs:\n%s", out)
}

// SkipUnlessEnv skips unless the named env var is "1".
func SkipUnlessEnv(t *testing.T, name string) {
	t.Helper()
	if os.Getenv(name) != "1" {
		t.Skipf("set %s=1 to run Docker stack citest", name)
	}
}

// RequireNoDiffDuplicateKeyInLogs asserts versiond logs do not contain the
// Postgres unique-violation signals from bare AppendDiff under HA catch-up.
func RequireNoDiffDuplicateKeyInLogs(t *testing.T, s *Stack, services ...string) {
	t.Helper()
	require.NotNil(t, s)
	out, err := s.ComposeLogsTail(400, services...)
	require.NoError(t, err, "compose logs")
	needles := []string{
		"23505",
		"duplicate key value violates unique constraint",
	}
	for _, needle := range needles {
		require.NotContains(t, out, needle,
			"versiond logs must not contain %q after HA catch-up (got %d bytes of logs)", needle, len(out))
	}
}
