package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "scripts", "check-storage-ddl.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("repo root not found (scripts/check-storage-ddl.sh missing)")
		}
		dir = parent
	}
}

func TestCheckStorageDDL_CleanTree(t *testing.T) {
	root := findRepoRoot(t)
	script := filepath.Join(root, "scripts", "check-storage-ddl.sh")

	cmd := exec.Command("bash", script)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "check-storage-ddl.sh failed:\n%s", string(out))
}
