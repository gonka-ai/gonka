package citest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGeneratedComposeConfigValid(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}

	testenvDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	workDir, err := os.MkdirTemp(testenvDir, "citest-compose-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	cfgPath := filepath.Join(workDir, "config.yaml")
	outPath := filepath.Join(workDir, "docker-compose.yml")

	gen := exec.Command("go", "run", "./cmd/gencompose", "-config", cfgPath, "-out", outPath)
	gen.Dir = testenvDir
	out, err := gen.CombinedOutput()
	if err != nil {
		t.Fatalf("gencompose: %v\n%s", err, out)
	}

	composeCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	check := exec.CommandContext(composeCtx, "docker", "compose", "-f", outPath, "config")
	check.Dir = workDir
	out, err = check.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
}
