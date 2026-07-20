package harness

import (
	"os"
	"testing"
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
