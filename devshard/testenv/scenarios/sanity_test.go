package scenarios

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"
	"devshard/testenv/internal/testenvcfg"
)

// TestScenarios_ConfigPresent wires make ci-scenarios to a real go test. Full C1…C14
// protocol cases are integration-heavy; they run here once a harness exists (testenv.md §8.3).
func TestScenarios_ConfigPresent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := testenvcfg.GenerateFilledMaterializedConfig(t, dir)
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
	compose := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(compose); err != nil {
		t.Fatalf("gencompose should write docker-compose.yml next to config: %v", err)
	}
}
