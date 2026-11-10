package harness

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// GetMetricsBody returns the Prometheus exposition text from metricsURL.
func GetMetricsBody(t *testing.T, client *http.Client, metricsURL string) string {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Get(metricsURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s: %s", metricsURL, string(body))
	return string(body)
}

// MetricLineValue finds the first exposition sample whose name matches metric
// and whose label set contains every required label. Returns false when absent.
func MetricLineValue(body, metric string, labels map[string]string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, "{")
		if !ok {
			name, rest, ok = strings.Cut(line, " ")
			if !ok || name != metric || len(labels) > 0 {
				continue
			}
			v, err := strconv.ParseFloat(strings.Fields(rest)[0], 64)
			if err != nil {
				continue
			}
			return v, true
		}
		if name != metric {
			continue
		}
		labelPart, valuePart, ok := strings.Cut(rest, "}")
		if !ok {
			continue
		}
		if !labelsMatch(labelPart, labels) {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(valuePart))
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		return v, true
	}
	return 0, false
}

func labelsMatch(labelPart string, want map[string]string) bool {
	for k, v := range want {
		needle := fmt.Sprintf(`%s="%s"`, k, v)
		if !strings.Contains(labelPart, needle) {
			return false
		}
	}
	return true
}

// MetricHasLabel reports whether any sample of metric carries label=value.
func MetricHasLabel(body, metric, label, value string) bool {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(metric) + `\{[^}]*` +
		regexp.QuoteMeta(label) + `="` + regexp.QuoteMeta(value) + `"`)
	return re.MatchString(body)
}

const heightSyncMetricPrefix = "devshard_gateway_heightsync_"

// AnyMetricHasLabel reports whether any series in the exposition carries label=value.
func AnyMetricHasLabel(body, label, value string) bool {
	return metricBodyHasLabel(body, "", label, value)
}

// AnyHeightSyncMetricHasLabel is H47's compose scrape: only the §8.12 height-sync
// family. Picker/receipt CounterVecs keep process-lifetime series and are not
// part of retireRuntime's ConstMetric drop.
func AnyHeightSyncMetricHasLabel(body, label, value string) bool {
	return metricBodyHasLabel(body, heightSyncMetricPrefix, label, value)
}

func metricBodyHasLabel(body, namePrefix, label, value string) bool {
	needle := fmt.Sprintf(`%s="%s"`, label, value)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if namePrefix != "" && !strings.HasPrefix(line, namePrefix) {
			continue
		}
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// WaitMetricsPredicate polls gateway /metrics until pred returns true.
func WaitMetricsPredicate(t *testing.T, client *http.Client, metricsURL string, timeout time.Duration, pred func(body string) bool) string {
	t.Helper()
	var last string
	ok := AssertEventually(t, timeout, 2*time.Second, func() bool {
		last = GetMetricsBody(t, client, metricsURL)
		return pred(last)
	})
	require.True(t, ok, "metrics predicate not met within %s; last body excerpt:\n%s", timeout, truncate(last, 2_000))
	return last
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// GetDebugHeightSync fetches admin-gated GET /v1/debug/heightsync.
func GetDebugHeightSync(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, dest any) {
	t.Helper()
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/debug/heightsync", adminAPIKey, dest))
}
