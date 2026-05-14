// Package testenvcfg provides isolated testenv config generation for tests.
// It shells out to cmd/gencompose so behaviour matches operators and CI
// (defaults, key fill, validate, compose emit) without committing config.yaml.
package testenvcfg

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// devshardModuleRoot returns the devshard module directory (directory that
// contains this module's go.mod). The caller file must live under
// devshard/testenv/... (e.g. internal/testenvcfg).
func devshardModuleRoot(t *testing.T, callerFile string) string {
	t.Helper()
	dir := filepath.Dir(callerFile)
	// Walk up until go.mod names devshard.
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if fi, err := os.Stat(filepath.Join(d, "go.mod")); err == nil && !fi.IsDir() {
			return d
		}
	}
	t.Fatalf("go.mod not found above %s", dir)
	return ""
}

// TestenvDir is the devshard/testenv directory (contains cmd/gencompose).
func TestenvDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := devshardModuleRoot(t, file)
	return filepath.Join(root, "testenv")
}

// GenerateFilledMaterializedConfig runs gencompose with a config path that
// does not exist yet inside dstDir. gencompose bootstraps from built-in
// defaults, fills keys, validates, writes config.yaml and docker-compose.yml
// under dstDir. Returns the absolute path to the generated config.yaml.
func GenerateFilledMaterializedConfig(t *testing.T, dstDir string) string {
	t.Helper()
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dstDir, "config.yaml")
	outPath := filepath.Join(dstDir, "docker-compose.yml")
	te := TestenvDir(t)
	cmd := exec.Command("go", "run", "./cmd/gencompose", "-config", cfgPath, "-out", outPath)
	cmd.Dir = te
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gencompose (dir=%s): %v\n%s", te, err, out)
	}
	return cfgPath
}
