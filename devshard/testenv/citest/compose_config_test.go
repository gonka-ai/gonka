//go:build testenvci

// Package citest holds opt-in checks for make ci-integration (Phase 15).
// Build: go test -tags=testenvci ./testenv/citest/...
//
// TestGeneratedComposeConfigValid requires Docker. Steps use bounded contexts so
// a stopped/wedged Docker Desktop cannot hang the test run forever.
package citest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"devshard/testenv/internal/testenvcfg"
)

// TestGeneratedComposeConfigValid runs gencompose (isolated config copy), then
// `docker compose config` to validate merge/YAML. §8.7 full HTTP smoke is separate.
func TestGeneratedComposeConfigValid(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	// Tests run with cwd = this package (…/testenv/citest).
	testenvDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(testenvDir, "cmd", "gencompose")); err != nil {
		t.Fatalf("unexpected layout: %v", err)
	}

	// Generated compose uses `build.context: ..` (devshard module root). The
	// compose file must live under testenv/ so `..` resolves correctly.
	workDir, err := os.MkdirTemp(testenvDir, "citest-compose-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	testenvcfg.GenerateFilledMaterializedConfig(t, workDir)
	outPath := filepath.Join(workDir, "docker-compose.yml")

	// `docker compose config` talks to the engine; cap wait so a bad daemon
	// does not block `go test` indefinitely.
	composeCtx, composeCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer composeCancel()
	check := exec.CommandContext(composeCtx, "docker", "compose", "-f", outPath, "config")
	check.Dir = testenvDir
	out, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
}
