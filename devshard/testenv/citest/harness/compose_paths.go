package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fixComposePaths rewrites gencompose-relative build/volume paths so docker compose
// works when the compose file lives outside testenv/ (citest temp workdirs).
func fixComposePaths(t *testing.T, composePath, testenvDir string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join(testenvDir, "..", ".."))
	require.NoError(t, err)

	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	text := string(body)

	repl := []struct{ old, new string }{
		{"context: ../../versiond-router", "context: " + filepath.Join(repoRoot, "versiond-router")},
		{"context: ../../versioned", "context: " + filepath.Join(repoRoot, "versioned")},
		{"context: ../..", "context: " + repoRoot},
		{"- ../../build/devshardd:", "- " + filepath.Join(repoRoot, "build", "devshardd") + ":"},
	}
	for _, r := range repl {
		text = strings.ReplaceAll(text, r.old, r.new)
	}

	require.NoError(t, os.WriteFile(composePath, []byte(text), 0o644))
}
