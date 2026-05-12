package observability

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDashboardsLint asserts that every decentralized_api_* metric name
// referenced by a Grafana dashboard JSON in
// deploy/join/observability/grafana/dashboards/ is declared somewhere in
// decentralized-api Go source. Catches typos and renames.
//
// Unlike the devshard sister test, this one reads metric names from source
// rather than instantiating the full observability stack.
func TestDashboardsLint(t *testing.T) {
	repoRoot := findRepoRoot(t)
	dashDir := filepath.Join(repoRoot, "deploy", "join", "observability", "grafana", "dashboards")

	declared := collectDeclaredMetricNames(t, filepath.Join(repoRoot, "decentralized-api"))

	entries, err := os.ReadDir(dashDir)
	if err != nil {
		t.Fatalf("read dashboards dir: %v", err)
	}

	expr := regexp.MustCompile(`decentralized_api_[a-zA-Z0-9_]*`)
	suffixes := []string{"_bucket", "_sum", "_count"}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dashDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		matches := expr.FindAllString(string(data), -1)
		seen := make(map[string]struct{})
		for _, m := range matches {
			if m == "decentralized_api" || m == "decentralized_api_" {
				continue
			}
			seen[m] = struct{}{}
		}
		for metric := range seen {
			if _, ok := declared[metric]; ok {
				continue
			}
			matched := false
			for _, suf := range suffixes {
				if strings.HasSuffix(metric, suf) {
					if _, ok := declared[strings.TrimSuffix(metric, suf)]; ok {
						matched = true
						break
					}
				}
			}
			if !matched {
				t.Errorf("dashboard %s references undeclared metric %q", e.Name(), metric)
			}
		}
	}
}

// findRepoRoot walks up from cwd until it finds the deploy/ folder.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "deploy", "join", "observability")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root containing deploy/join/observability from cwd")
	return ""
}

// collectDeclaredMetricNames walks the decentralized-api tree and pulls out
// every "decentralized_api_*" string literal. False positives are fine —
// we only need declared ⊇ referenced.
func collectDeclaredMetricNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	literal := regexp.MustCompile(`"(decentralized_api_[a-zA-Z0-9_]+)"`)
	declared := make(map[string]struct{})
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip generated and vendor.
		if strings.Contains(path, "/vendor/") || strings.HasSuffix(path, ".pb.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range literal.FindAllStringSubmatch(string(data), -1) {
			declared[m[1]] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk decentralized-api: %v", err)
	}
	if len(declared) == 0 {
		t.Fatalf("found no declared decentralized_api_* metric names — walk root may be wrong")
	}
	return declared
}
