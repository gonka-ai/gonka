package citest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if strings.Contains("\n"+string(out), "\n  proxy:\n") {
		t.Fatal("default compose config includes proxy; citest-stack must stay no-proxy")
	}

	svcCtx, svcCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer svcCancel()
	svc := exec.CommandContext(svcCtx, "docker", "compose", "-f", outPath, "config", "--services")
	svc.Dir = workDir
	svcOut, err := svc.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose config --services: %v\n%s", err, svcOut)
	}
	for _, name := range strings.Fields(string(svcOut)) {
		if name == "proxy" {
			t.Fatal("default compose services include proxy")
		}
	}

	overlay := filepath.Join(testenvDir, "docker-compose.proxy.yml")
	ovCtx, ovCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ovCancel()
	ov := exec.CommandContext(ovCtx, "docker", "compose",
		"-f", outPath, "-f", overlay, "config", "--services")
	ov.Dir = workDir
	ovOut, err := ov.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose overlay config --services: %v\n%s", err, ovOut)
	}
	found := false
	for _, name := range strings.Fields(string(ovOut)) {
		if name == "proxy" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("overlay compose services missing proxy:\n%s", ovOut)
	}
}
