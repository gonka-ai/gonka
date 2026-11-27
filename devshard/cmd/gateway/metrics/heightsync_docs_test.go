package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"devshard/cmd/gateway/registry"
)

// Test flow:
//  1. Register a `HeightSyncCollector` over a published view and gather every family it emits.
//  2. Read the observability reference and the two height-sync documents and collect each `devshard_gateway_heightsync_` family name they spell out.
//  3. Assert every named family is one the collector emitted, so a document cannot name a series no scrape returns.
func TestEveryHeightSyncMetricTheDocumentsNameIsEmitted(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewHeightSyncCollector(heightSyncEscrows{
		fixedEscrows: fixedEscrows{states: []registry.EscrowState{{ID: "7"}}},
		sessions:     map[string]registry.EscrowSession{"7": viewSession{view: publishedHeightSyncView()}},
	}))
	gathered, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	emitted := map[string]bool{}
	for _, gatheredFamily := range gathered {
		emitted[gatheredFamily.GetName()] = true
	}

	named := regexp.MustCompile(`devshard_gateway_heightsync_[a-z][a-z_]*[a-z]`)
	documents := []string{
		filepath.Join("..", "docs", "observability.md"),
		filepath.Join("..", "..", "..", "docs", "v5-manual-height-sync.md"),
		filepath.Join("..", "..", "..", "docs", "height-sync-implementation-plan.md"),
	}
	for _, document := range documents {
		text, err := os.ReadFile(document)
		if err != nil {
			t.Fatalf("read %s: %v", document, err)
		}
		names := named.FindAllString(string(text), -1)
		if len(names) == 0 {
			t.Fatalf("%s names no height-sync family, so this check reads nothing", document)
		}
		for _, name := range names {
			if !emitted[name] {
				t.Errorf("%s names %s, which no scrape returns", document, name)
			}
		}
	}
}
