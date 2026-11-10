package harness

import (
	"bufio"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ParsedMetric is one sample from a Prometheus text exposition.
type ParsedMetric struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// FetchMetricsText GETs a Prometheus exposition endpoint.
func FetchMetricsText(t *testing.T, client *http.Client, metricsURL string) string {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Get(metricsURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s", metricsURL)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// ParseMetricsText parses a subset of Prometheus text format (gauges/counters/untyped).
// Histogram/summary lines with `le`/`quantile` labels are included as ordinary samples.
func ParseMetricsText(body string) []ParsedMetric {
	var out []ParsedMetric
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, ok := parseMetricLine(line)
		if !ok {
			continue
		}
		out = append(out, ParsedMetric{Name: name, Labels: labels, Value: value})
	}
	return out
}

func parseMetricLine(line string) (name string, labels map[string]string, value float64, ok bool) {
	// name{labels} value  OR  name value
	rest := line
	brace := strings.IndexByte(rest, '{')
	if brace >= 0 {
		name = rest[:brace]
		closeBrace := strings.LastIndexByte(rest, '}')
		if closeBrace <= brace {
			return "", nil, 0, false
		}
		labels = parseLabelSet(rest[brace+1 : closeBrace])
		rest = strings.TrimSpace(rest[closeBrace+1:])
	} else {
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			return "", nil, 0, false
		}
		name = fields[0]
		rest = fields[1]
		labels = map[string]string{}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", nil, 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", nil, 0, false
	}
	return name, labels, v, true
}

func parseLabelSet(raw string) map[string]string {
	out := map[string]string{}
	for len(raw) > 0 {
		raw = strings.TrimLeft(raw, ", ")
		if raw == "" {
			break
		}
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			break
		}
		key := raw[:eq]
		raw = raw[eq+1:]
		if !strings.HasPrefix(raw, `"`) {
			break
		}
		raw = raw[1:]
		var b strings.Builder
		for i := 0; i < len(raw); i++ {
			if raw[i] == '\\' && i+1 < len(raw) {
				b.WriteByte(raw[i+1])
				i++
				continue
			}
			if raw[i] == '"' {
				out[key] = b.String()
				raw = raw[i+1:]
				break
			}
			b.WriteByte(raw[i])
		}
	}
	return out
}

func labelsContain(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// FindMetric returns the first sample matching name + required labels.
func FindMetric(metrics []ParsedMetric, name string, labels map[string]string) (ParsedMetric, bool) {
	for _, m := range metrics {
		if m.Name != name {
			continue
		}
		if labelsContain(m.Labels, labels) {
			return m, true
		}
	}
	return ParsedMetric{}, false
}

// MetricHasLabelValue reports whether any sample of name has label=value.
func MetricHasLabelValue(metrics []ParsedMetric, name, label, value string) bool {
	for _, m := range metrics {
		if m.Name == name && m.Labels[label] == value {
			return true
		}
	}
	return false
}

// RequireNoMetric fails if any sample of name has all of labels.
func RequireNoMetric(t *testing.T, metrics []ParsedMetric, name string, labels map[string]string) {
	t.Helper()
	if _, ok := FindMetric(metrics, name, labels); ok {
		t.Fatalf("metric %s labels %v still present", name, labels)
	}
}

// WaitMetricGauge polls until a gauge matching name+labels satisfies pred.
func WaitMetricGauge(t *testing.T, client *http.Client, metricsURL, name string, labels map[string]string, pred func(float64) bool, timeout time.Duration) float64 {
	t.Helper()
	var last float64
	require.Eventually(t, func() bool {
		body := FetchMetricsText(t, client, metricsURL)
		metrics := ParseMetricsText(body)
		m, ok := FindMetric(metrics, name, labels)
		if !ok {
			return false
		}
		last = m.Value
		return pred(m.Value)
	}, timeout, 200*time.Millisecond, "wait gauge %s %v", name, labels)
	return last
}

// WaitMetricAbsent polls until no sample matches name+labels.
func WaitMetricAbsent(t *testing.T, client *http.Client, metricsURL, name string, labels map[string]string, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		body := FetchMetricsText(t, client, metricsURL)
		_, ok := FindMetric(ParseMetricsText(body), name, labels)
		return !ok
	}, timeout, 200*time.Millisecond, "wait absent %s %v", name, labels)
}

// HistogramSampleCount returns `_count` for a histogram (name_count or sample with no le label).
func HistogramSampleCount(metrics []ParsedMetric, name string) float64 {
	if m, ok := FindMetric(metrics, name+"_count", nil); ok {
		return m.Value
	}
	// Some expositions emit the histogram as name_bucket + name_count.
	return 0
}
