package observability

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestDashboardsLint asserts that every Prometheus metric name referenced by
// a Grafana dashboard JSON in deploy/join/observability/grafana/dashboards/
// is actually registered in the devshard Prometheus registry. This catches
// typos like "..._gb" and metrics that get renamed without updating
// dashboards.
//
// The test inspects dashboards whose filename contains "devshard" since
// other dashboards target chain/node-health metrics outside this registry.
func TestDashboardsLint(t *testing.T) {
	repoRoot := findRepoRoot(t)
	dashDir := filepath.Join(repoRoot, "deploy", "join", "observability", "grafana", "dashboards")

	entries, err := os.ReadDir(dashDir)
	if err != nil {
		t.Fatalf("read dashboards dir: %v", err)
	}

	// Force lazy-registered prom instruments to register.
	initPromMetrics()
	ensureMetrics()

	registered := registeredMetricNames(t, Registry())

	// Histogram-derived suffixes that prometheus exports automatically.
	suffixes := []string{"_bucket", "_sum", "_count"}

	expr := regexp.MustCompile(`devshard[a-zA-Z0-9_]*`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// Limit to dashboards that target devshard metrics.
		if !strings.Contains(e.Name(), "devshard") {
			continue
		}

		path := filepath.Join(dashDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		// Extract every devshard* identifier referenced in the dashboard
		// JSON (Grafana embeds full PromQL in panel target exprs).
		matches := expr.FindAllString(string(data), -1)
		seen := make(map[string]struct{})
		for _, m := range matches {
			if m == "devshard" || m == "devshardd" {
				continue // bare prefixes appear in label selectors, not metric names
			}
			seen[m] = struct{}{}
		}

		for metric := range seen {
			if _, ok := registered[metric]; ok {
				continue
			}
			// Strip histogram suffix and recheck.
			matched := false
			for _, suf := range suffixes {
				if strings.HasSuffix(metric, suf) {
					if _, ok := registered[strings.TrimSuffix(metric, suf)]; ok {
						matched = true
						break
					}
				}
			}
			if !matched {
				t.Errorf("dashboard %s references unregistered metric %q", e.Name(), metric)
			}
		}
	}
}

// findRepoRoot walks up from cwd until it finds a directory containing
// the deploy/ folder we need.
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

// registeredMetricNames gathers all metric names currently registered with
// the registry. Histograms are returned by their base name (no _bucket suffix).
func registeredMetricNames(t *testing.T, reg prometheus.Gatherer) map[string]struct{} {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := make(map[string]struct{}, len(mfs))
	// Some metrics have no observations yet so Gather() omits them. To be
	// safe we also accept descriptors via reflection on the registry's
	// Describe channel.
	for _, mf := range mfs {
		names[mf.GetName()] = struct{}{}
	}

	// Pull descriptors directly so unobserved metrics are still considered
	// registered.
	if pr, ok := reg.(*prometheus.Registry); ok {
		descCh := make(chan *prometheus.Desc, 256)
		go func() {
			defer close(descCh)
			pr.Describe(descCh)
		}()
		descRe := regexp.MustCompile(`fqName: \"([^\"]+)\"`)
		for d := range descCh {
			s := d.String()
			if m := descRe.FindStringSubmatch(s); len(m) == 2 {
				names[m[1]] = struct{}{}
			}
		}
	}
	return names
}
