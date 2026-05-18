//go:build testenvci

package container

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMain(m *testing.M) {
	// run-container-heightsync-e2e.sh runs gencompose before compose up; skip duplicate regen.
	if os.Getenv("TESTENV_SKIP_DOCKER_STACK") != "1" &&
		os.Getenv("TESTENV_REUSE_STACK") != "1" &&
		os.Getenv("SKIP_REGEN") != "1" {
		_, file, _, ok := runtime.Caller(0)
		if ok {
			testenvDir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
			cmd := exec.Command("go", "run", "./cmd/gencompose", "-config", "config.yaml")
			cmd.Dir = testenvDir
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "container TestMain: gencompose: %v\n", err)
			}
		}
	}
	os.Exit(m.Run())
}
