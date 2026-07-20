package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixComposePaths_AbsoluteContexts(t *testing.T) {
	repoRoot := t.TempDir()
	testenvDir := filepath.Join(repoRoot, "devshard", "testenv")
	require.NoError(t, os.MkdirAll(testenvDir, 0o755))
	composePath := filepath.Join(testenvDir, "docker-compose.yml")

	original := `services:
  mock-chain:
    build:
      context: ../..
      dockerfile: devshard/testenv/Dockerfile.mock-chain
  versiond-0:
    build:
      context: ../../versioned
    volumes:
      - ../../build/devshardd:/opt/devshard/devshardd:ro
  versiond-router:
    build:
      context: ../../versiond-router
`
	require.NoError(t, os.WriteFile(composePath, []byte(original), 0o644))

	fixComposePaths(t, composePath, testenvDir)

	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "context: "+filepath.Join(repoRoot, "versiond-router"))
	require.Contains(t, text, "context: "+filepath.Join(repoRoot, "versioned"))
	require.Contains(t, text, "context: "+repoRoot)
	require.Contains(t, text, "- "+filepath.Join(repoRoot, "build", "devshardd")+":")
	require.NotContains(t, text, "context: ../..")
}
