package chainoracle_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoForbiddenImports ensures chainoracle does not depend on testenv, host,
// or heightsync (Phase 2 exit criterion).
func TestNoForbiddenImports(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(thisFile)
	forbidden := []string{
		"devshard/testenv",
		"devshard/host",
		"devshard/heightsync",
	}
	var violations []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "import_gate_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, `"devshard/`) {
				continue
			}
			for _, f := range forbidden {
				if strings.Contains(line, f) {
					violations = append(violations, path+": "+line)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("forbidden imports:\n%s", strings.Join(violations, "\n"))
	}
}
