package scenarios

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"
)

// TestScenarios_ConfigPresent wires make ci-scenarios to a real go test. Full C1…C14
// protocol cases are integration-heavy; they run here once a harness exists (testenv.md §8.3).
func TestScenarios_ConfigPresent(t *testing.T) {
	t.Parallel()
	testenvDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(testenvDir, "config.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("testenv config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(cfg.HeightSync.Validators) < 1 {
		t.Fatal("height_sync.validators must be non-empty for scenario work")
	}
}
