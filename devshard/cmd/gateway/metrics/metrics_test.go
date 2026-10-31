package metrics

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Test flow:
//  1. Wrap a handler that always answers 418 with `InstrumentRoute`.
//  2. Serve one GET request through it.
//  3. Assert the response status passes through unchanged.
//  4. Scrape the exposition and assert it carries the request counter and duration histogram for the route.
func TestInstrumentRouteCountsRequestsWithPreservedFamilyNames(t *testing.T) {
	gatewayMetrics := New()
	instrumented := gatewayMetrics.InstrumentRoute("/v1/test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	recorder := httptest.NewRecorder()
	instrumented.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("instrumented handler must pass through status, got %d", recorder.Code)
	}

	exposition := scrape(t, gatewayMetrics)
	wantCounter := `devshard_http_requests_total{method="GET",path="/v1/test",status="418"} 1`
	if !strings.Contains(exposition, wantCounter) {
		t.Fatalf("exposition missing %q\n---\n%s", wantCounter, exposition)
	}
	if !strings.Contains(exposition, `devshard_http_request_duration_seconds_count{method="GET",path="/v1/test"} 1`) {
		t.Fatalf("exposition missing duration histogram for the route\n---\n%s", exposition)
	}
}

// Test flow:
//  1. Scrape the metrics handler's exposition.
//  2. Assert it includes the Go runtime collector's `go_goroutines` series.
func TestHandlerServesGoRuntimeCollectors(t *testing.T) {
	gatewayMetrics := New()
	exposition := scrape(t, gatewayMetrics)
	if !strings.Contains(exposition, "go_goroutines") {
		t.Fatal("exposition missing go_goroutines — Go collector not registered")
	}
}

// Test flow:
//  1. Wrap a handler that writes a body without calling `WriteHeader` explicitly.
//  2. Serve one POST request through it.
//  3. Scrape the exposition and assert the request was recorded with the implicit 200 status.
func TestDefaultStatusIsRecordedAs200WhenHandlerWritesBody(t *testing.T) {
	gatewayMetrics := New()
	instrumented := gatewayMetrics.InstrumentRoute("/plain", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	recorder := httptest.NewRecorder()
	instrumented.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/plain", nil))

	exposition := scrape(t, gatewayMetrics)
	if !strings.Contains(exposition, `devshard_http_requests_total{method="POST",path="/plain",status="200"} 1`) {
		t.Fatalf("implicit 200 not recorded\n---\n%s", exposition)
	}
}

func scrape(t *testing.T, gatewayMetrics *Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	gatewayMetrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("reading exposition: %v", err)
	}
	return string(body)
}

// Test flow:
//  1. Wrap a handler behind `InstrumentRoute` with route "other".
//  2. Serve 200 requests, each using a distinct client-chosen HTTP method, plus one ordinary GET.
//  3. Gather the request-count metric family.
//  4. Assert it holds only 2 series total (the bounded "other" method plus GET), not one per client-chosen method.
func TestInstrumentRouteBoundsTheMethodLabel(t *testing.T) {
	telemetry := New()
	handler := telemetry.InstrumentRoute("other", http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))

	for probe := range 200 {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Method = fmt.Sprintf("PROBE%d", probe)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	families, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather(): %v", err)
	}
	for _, family := range families {
		if family.GetName() != "devshard_http_requests_total" {
			continue
		}
		if series := len(family.GetMetric()); series != 2 {
			t.Fatalf("200 client-chosen methods produced %d series, want 2 (other + GET)", series)
		}
		return
	}
	t.Fatal("devshard_http_requests_total was not registered")
}
