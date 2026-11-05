package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"devshard/cmd/gateway/metrics"
)

var (
	// legacyRouteLabels is the label domain the existing dashboards select on.
	legacyRouteLabels = []string{
		"/v1/models",
		"/v1/chat/completions",
		"/v1/status",
		"/v1/admin/state",
		"/v1/admin/settings",
		"/v1/admin/devshards",
		"/v1/admin/devshards/{id}",
		"/v1/admin/devshards/{id}/activate",
		"/v1/admin/devshards/{id}/deactivate",
		"/v1/admin/devshards/{id}/settle",
		"/v1/admin/devshards/{id}/participants",
		"/v1/admin/escrows",
		"/v1/admin/suspicious-hosts",
		"/v1/admin/participants/unquarantine",
		"/v1/admin/accounting/reset/{epoch}",
		"/v1/debug/rotation",
		"/v1/debug/heightsync",
		"/v1/debug/memstats",
		"/devshard/{id}/",
		"/devshard/{id}/openapi.json",
		"/devshard/{id}/v1/models",
		"/devshard/{id}/v1/chat/completions",
		"/devshard/{id}/v1/status",
		"/devshard/{id}/v1/state",
		"/devshard/{id}/v1/finalize",
		"/devshard/{id}/v1/debug/pending",
		"/devshard/{id}/v1/debug/state",
		"/devshard/{id}/v1/debug/inferences",
		"/devshard/{id}/v1/debug/perf",
		"/devshard/{id}/v1/debug/pairwise",
		"/devshard/{id}/v1/debug/signatures",
		"/devshard/{id}/v1/debug/signatures/collect",
		"/devshard/{id}/v1/debug/sync-hosts",
	}

	// deliberateDivergences are the only labels allowed to sit outside the devshardctl domain.
	deliberateDivergences = []string{otherRouteLabel, "/v1/requests/{id}", "/v1/admin/hosts", "/v1/admin/devshards/settle", "/v1/admin/devshards/deactivate", "/v1/admin/filters", "/v1/admin/filters/presets", "/v1/admin/filters/{model}"}
)

// Test flow:
//  1. Build a map pinning every expected route pattern to its metric label.
//  2. Read the registered routes off a harness server.
//  3. Assert the registered route count matches the pinned map.
//  4. Assert every registered route's label matches its pinned entry.
func TestEveryRouteCarriesItsExactMetricLabel(t *testing.T) {
	expected := map[string]string{
		"/v1/models":                                 "/v1/models",
		"/v1/chat/completions":                       "/v1/chat/completions",
		"/v1/status":                                 "/v1/status",
		"/metrics":                                   "",
		"/healthz":                                   "",
		"/devshard/{id}/v1/models":                   "/devshard/{id}/v1/models",
		"/devshard/{id}/v1/chat/completions":         "/devshard/{id}/v1/chat/completions",
		"/devshard/{id}/v1/status":                   "/devshard/{id}/v1/status",
		"/devshard/{id}/v1/finalize":                 "/devshard/{id}/v1/finalize",
		"/devshard/{id}/v1/state":                    "/devshard/{id}/v1/state",
		"/devshard/{id}/v1/debug/state":              "/devshard/{id}/v1/debug/state",
		"/devshard/{id}/v1/debug/inferences":         "/devshard/{id}/v1/debug/inferences",
		"/devshard/{id}/v1/debug/pending":            "/devshard/{id}/v1/debug/pending",
		"/devshard/{id}/v1/debug/signatures":         "/devshard/{id}/v1/debug/signatures",
		"/devshard/{id}/v1/debug/signatures/collect": "/devshard/{id}/v1/debug/signatures/collect",
		"/v1/requests/{id}":                          "/v1/requests/{id}",
		"/v1/admin/state":                            "/v1/admin/state",
		"/v1/admin/settings":                         "/v1/admin/settings",
		"/v1/admin/devshards":                        "/v1/admin/devshards",
		"/v1/admin/devshards/import":                 "/v1/admin/devshards/{id}",
		"/v1/admin/devshards/settle":                 "/v1/admin/devshards/settle",
		"/v1/admin/devshards/deactivate":             "/v1/admin/devshards/deactivate",
		"/v1/admin/devshards/{id}":                   "/v1/admin/devshards/{id}",
		"/v1/admin/devshards/{id}/activate":          "/v1/admin/devshards/{id}/activate",
		"/v1/admin/devshards/{id}/deactivate":        "/v1/admin/devshards/{id}/deactivate",
		"/v1/admin/devshards/{id}/settle":            "/v1/admin/devshards/{id}/settle",
		"/v1/admin/devshards/{id}/participants":      "/v1/admin/devshards/{id}/participants",
		"/v1/admin/escrows":                          "/v1/admin/escrows",
		"/v1/admin/suspicious-hosts":                 "/v1/admin/suspicious-hosts",
		"/v1/admin/filters":                          "/v1/admin/filters",
		"/v1/admin/filters/presets":                  "/v1/admin/filters/presets",
		"/v1/admin/filters/{model...}":               "/v1/admin/filters/{model}",
		"/v1/admin/participants/unquarantine":        "/v1/admin/participants/unquarantine",
		"/v1/admin/accounting/reset/{epoch}":         "/v1/admin/accounting/reset/{epoch}",
		"/v1/admin/hosts":                            "/v1/admin/hosts",
		"/v1/debug/rotation":                         "/v1/debug/rotation",
		"/v1/debug/heightsync":                       "/v1/debug/heightsync",
		"/v1/debug/memstats":                         "/v1/debug/memstats",
		"/debug/pprof/":                              "",
		"/debug/pprof/cmdline":                       "",
		"/debug/pprof/profile":                       "",
		"/debug/pprof/symbol":                        "",
		"/debug/pprof/trace":                         "",
		"/":                                          otherRouteLabel,
	}

	live := newHarness(t)
	registered := live.server.routes()
	if len(registered) != len(expected) {
		t.Fatalf("route count: got %d, want %d — a new route needs its label pinned here", len(registered), len(expected))
	}
	for _, route := range registered {
		want, known := expected[route.pattern]
		if !known {
			t.Fatalf("route %q has no pinned label", route.pattern)
		}
		if route.label != want {
			t.Fatalf("route %q label: got %q, want %q", route.pattern, route.label, want)
		}
	}
}

// Test flow:
//  1. Walk every registered route on a harness server.
//  2. Assert every non-empty label is either in `legacyRouteLabels` or `deliberateDivergences`.
func TestEveryInstrumentedLabelIsALegacyLabelOrADeclaredDivergence(t *testing.T) {
	live := newHarness(t)
	for _, route := range live.server.routes() {
		if route.label == "" {
			continue
		}
		if slices.Contains(legacyRouteLabels, route.label) || slices.Contains(deliberateDivergences, route.label) {
			continue
		}
		t.Fatalf("route %q emits label %q, which is neither a devshardctl label nor a declared divergence", route.pattern, route.label)
	}
}

// Test flow:
//  1. Define a table of unmatched paths, varying across a favicon request, an unmatched devshard subpath, and an unmatched devshard request-id subpath.
//  2. For each probe, send a GET request through a fresh harness.
//  3. Assert the response is 404.
//  4. Assert the telemetry-served label is `otherRouteLabel`.
func TestUnmatchedPathsFoldIntoOneLabel(t *testing.T) {
	probes := []string{
		"/favicon.ico",
		"/devshard/abc/",
		"/devshard/abc/v1/requests/9f2c1d4e-0000-4444-8888-aaaabbbbcccc",
	}
	for _, probe := range probes {
		t.Run(probe, func(t *testing.T) {
			live := newHarness(t)
			recorder := live.request(t, http.MethodGet, probe, "", nil)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status: got %d, want 404", recorder.Code)
			}
			if got := live.telemetry.served; got != otherRouteLabel {
				t.Fatalf("label for %q: got %q, want %q", probe, got, otherRouteLabel)
			}
		})
	}
}

// Test flow:
//  1. Define a table of traversal probe paths using dot-dot segments.
//  2. For each probe, send a GET request through a fresh harness.
//  3. Assert the response is a path-cleaning redirect (301 or 307), answered above every registered route.
//  4. Assert no telemetry label was served.
func TestATraversalProbeReachesNoLabel(t *testing.T) {
	for _, probe := range []string{"/../etc/passwd", "/v1/models/../../secret"} {
		t.Run(probe, func(t *testing.T) {
			live := newHarness(t)
			recorder := live.request(t, http.MethodGet, probe, "", nil)
			if recorder.Code != http.StatusMovedPermanently && recorder.Code != http.StatusTemporaryRedirect {
				t.Fatalf("status: got %d, want a cleaning redirect", recorder.Code)
			}
			if live.telemetry.served != "" {
				t.Fatalf("label: got %q, want none", live.telemetry.served)
			}
		})
	}
}

// Test flow:
//  1. Send an admin-authorized GET request for a status by request ID.
//  2. Assert the served label is the templated `/v1/requests/{id}`, not the raw ID.
//  3. Assert none of the recorded labels contain the raw request ID.
func TestARequestIDNeverReachesALabel(t *testing.T) {
	requestID := "9f2c1d4e-0000-4444-8888-aaaabbbbcccc"
	live := newHarness(t)
	live.request(t, http.MethodGet, "/v1/requests/"+requestID, "", adminHeaders())
	if live.telemetry.served != "/v1/requests/{id}" {
		t.Fatalf("label: got %q, want %q", live.telemetry.served, "/v1/requests/{id}")
	}
	for _, label := range append(live.telemetry.labels, live.telemetry.served) {
		if strings.Contains(label, requestID) {
			t.Fatalf("label %q leaked the request id", label)
		}
	}
}

// Test flow:
//  1. Build a real `metrics.Metrics` telemetry and wire it into a harness server, replacing the harness's fake.
//  2. Scrape /metrics twice.
//  3. Send one instrumented request to /v1/models so the counted family exists at all.
//  4. Gather the `devshard_http_requests_total` and `devshard_http_request_duration_seconds` families.
//  5. Assert neither family carries a metric labelled with the /metrics path itself.
func TestMetricsScrapeDoesNotCountItself(t *testing.T) {
	telemetry := metrics.New()
	live := newHarness(t)
	live.server.telemetry = telemetry
	live.server.handler = live.server.buildHandler()

	for range 2 {
		recorder := httptest.NewRecorder()
		live.server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("scrape status: got %d", recorder.Code)
		}
	}
	live.server.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	requests := gatheredFamily(t, telemetry, "devshard_http_requests_total")
	if len(requests) == 0 {
		t.Fatal("devshard_http_requests_total is absent, so the assertion below proves nothing")
	}
	for _, metric := range requests {
		if labelValue(metric, "path") == "/metrics" {
			t.Fatalf("the scrape counted itself: %v", metric)
		}
	}
	for _, metric := range gatheredFamily(t, telemetry, "devshard_http_request_duration_seconds") {
		if labelValue(metric, "path") == "/metrics" {
			t.Fatalf("the duration histogram measured its own exposition: %v", metric)
		}
	}
}

func gatheredFamily(t *testing.T, telemetry *metrics.Metrics, name string) []*dto.Metric {
	t.Helper()
	families, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family.GetMetric()
		}
	}
	return nil
}

func labelValue(metric *dto.Metric, name string) string {
	for _, pair := range metric.GetLabel() {
		if pair.GetName() == name {
			return pair.GetValue()
		}
	}
	return ""
}
