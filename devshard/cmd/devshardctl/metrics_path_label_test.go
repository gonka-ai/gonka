package main

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNormalizeMetricsPathKeepsRoutes(t *testing.T) {
	cases := map[string]string{
		"/v1/chat/completions":                "/v1/chat/completions",
		"/v1/admin/devshards/12/settle":       "/v1/admin/devshards/{id}/settle",
		"/v1/admin/devshards/12/participants": "/v1/admin/devshards/{id}/participants",
		"/v1/admin/devshards/import":          "/v1/admin/devshards/{id}",
		"/devshard/12/v1/chat/completions":    "/devshard/{id}/v1/chat/completions",
		"/devshard/12/v1/requests/req-1":      "/devshard/{id}/v1/requests/{request_id}",
		"/debug/pprof/heap":                   "/debug/pprof/heap",
		"/v1/x00000001":                       metricsOtherLabel,
		"/devshard/12/zz":                     "/devshard/{id}/" + metricsOtherLabel,
		"/v1/admin/devshards/12/zz":           "/v1/admin/devshards/{id}/" + metricsOtherLabel,
	}
	for in, want := range cases {
		if got := normalizeMetricsPath(in); got != want {
			t.Errorf("normalizeMetricsPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := normalizeMetricsMethod("FOO"); got != metricsOtherLabel {
		t.Errorf("method FOO -> %q", got)
	}
}

func TestGatewayMetricsUnknownPathsShareSeries(t *testing.T) {
	g := NewGateway(nil, nil, "")
	h := buildGatewayHandler(g, runtimeOptions{apiKeys: map[string]struct{}{"k": {}}})
	for i := 0; i < 1000; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(fmt.Sprintf("M%d", i), fmt.Sprintf("/v1/x%d", i), nil))
	}
	if n := testutil.CollectAndCount(g.metrics.httpRequests); n != 1 {
		t.Fatalf("1000 unknown paths produced %d request series, want 1", n)
	}
}

func TestDirectChatRejectedModelUsesDevshardModelLabel(t *testing.T) {
	rt := &gatewayMockRuntime{id: "11", model: mockenvDefaultModel, active: true}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})
	for i := 0; i < 50; i++ {
		rec := env.postDirectChat("11", mockenvChatBody(fmt.Sprintf("client/model-%d", i), "hello"))
		if rec.Code < 400 {
			t.Fatalf("model %d was accepted: %d", i, rec.Code)
		}
	}
	if n := testutil.CollectAndCount(env.gateway.metrics.gatewayRequests); n != 1 {
		t.Fatalf("50 rejected client models produced %d request series, want 1", n)
	}
}
