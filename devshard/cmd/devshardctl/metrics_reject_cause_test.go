package main

import (
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// reject_cause comes from a peer's VerifyErrorMiss response (up to the 10 MiB
// large-read cap); it must not become a free-form label value.
func TestErrorMissVerifyRejectCauseIsBounded(t *testing.T) {
	m := NewDevshardMetrics()
	for i := 0; i < 50; i++ {
		m.RecordErrorMissVerifyReject(fmt.Sprintf("%d-%s", i, strings.Repeat("x", 64<<10)), "drift")
	}
	m.RecordErrorMissVerifyReject("hash_mismatch", "drift")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	t.Logf("/metrics body %d bytes", len(body))

	families, err := m.registry.Gather()
	require.NoError(t, err)
	series := 0
	for _, f := range families {
		if f.GetName() == "devshard_gateway_error_miss_verify_rejects_total" {
			series = len(f.GetMetric())
		}
	}
	require.LessOrEqual(t, series, 2, "one series per peer-chosen cause")
	requireMetricCounterValue(t, families, "devshard_gateway_error_miss_verify_rejects_total", map[string]string{"cause": "hash_mismatch", "completeness": "drift"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_error_miss_verify_rejects_total", map[string]string{"cause": "other", "completeness": "drift"}, 50)
}
