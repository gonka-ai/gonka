package api

import (
	"net/http"
	"strings"
	"testing"
)

// Test flow:
//  1. For each pprof target, varying across the index, goroutine dump, heap summary, command line and CPU profile, request it with the admin key.
//  2. Assert the response is 200.
//  3. Assert the body is non-empty, except for the command-line target.
//  4. Assert the body contains the expected marker text.
func TestTheProfilerAnswersTheAdmin(t *testing.T) {
	testCases := []struct {
		name   string
		target string
		want   string
	}{
		{name: "index", target: "/debug/pprof/", want: "goroutine"},
		{name: "goroutine dump as text", target: "/debug/pprof/goroutine?debug=1", want: "goroutine profile"},
		{name: "heap summary as text", target: "/debug/pprof/heap?debug=1", want: "heap profile"},
		{name: "command line", target: "/debug/pprof/cmdline", want: ""},
		{name: "cpu profile", target: "/debug/pprof/profile?seconds=1", want: ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)

			recorder := live.request(t, http.MethodGet, testCase.target, "", adminHeaders())

			if recorder.Code != http.StatusOK {
				t.Fatalf("%s: got %d %s, want 200", testCase.target, recorder.Code, recorder.Body.String())
			}
			if recorder.Body.Len() == 0 && testCase.name != "command line" {
				t.Fatalf("%s answered an empty body", testCase.target)
			}
			if !strings.Contains(recorder.Body.String(), testCase.want) {
				t.Fatalf("%s: body does not mention %q", testCase.target, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each pprof target, request it with a caller key that is not the admin key.
//  2. Assert the response is refused with 401 or 403.
func TestTheProfilerRefusesACallerWithoutTheAdminKey(t *testing.T) {
	for _, target := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/profile?seconds=1", "/debug/pprof/trace?seconds=1"} {
		t.Run(target, func(t *testing.T) {
			live := newHarness(t)

			recorder := live.request(t, http.MethodGet, target, "", callerHeaders("not-the-admin"))

			if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusForbidden {
				t.Fatalf("%s without the admin key: got %d, want it refused", target, recorder.Code)
			}
		})
	}
}

// Test flow:
//  1. Request the pprof goroutine endpoint with the admin key.
//  2. Assert no route-metrics label was recorded for it.
func TestTheProfilerIsNotPartOfTheRouteMetrics(t *testing.T) {
	live := newHarness(t)

	live.request(t, http.MethodGet, "/debug/pprof/goroutine?debug=1", "", adminHeaders())

	if got := live.telemetry.served; got != "" {
		t.Fatalf("a profiler request was counted under route label %q; the profiler carries no label", got)
	}
}
