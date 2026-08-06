package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// WaitTraceByAttr polls the active trace backend until at least one trace
// matches tagQuery, then returns the matching trace ids.
//
// Tempo: TraceQL queries of the form `{ span.x = "y" }` use /api/search?q=…
// (tags= cannot filter custom span attributes). Jaeger: the same TraceQL-shaped
// query is converted into /jaeger/api/traces?tags=… JSON.
func WaitTraceByAttr(t *testing.T, obs ObservabilityEndpoints, tagQuery string, timeout time.Duration) []string {
	t.Helper()
	ids := TryWaitTraceByAttr(t, obs, tagQuery, timeout)
	require.NotEmpty(t, ids, "%s traces matching %q not found within %s",
		obs.Profile.TraceBackend(), tagQuery, timeout)
	return ids
}

// TryWaitTraceByAttr is WaitTraceByAttr without failing the test on timeout.
func TryWaitTraceByAttr(t *testing.T, obs ObservabilityEndpoints, tagQuery string, timeout time.Duration) []string {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	require.NotEmpty(t, tagQuery)
	client := &http.Client{Timeout: 10 * time.Second}
	backend := obs.Profile.TraceBackend()
	t.Logf("citest: waiting for %s traces matching %q", backend, tagQuery)

	var ids []string
	_ = assertEventually(t, timeout, 3*time.Second, func() bool {
		var found []string
		switch backend {
		case "tempo":
			found = tempoSearchByAttr(client, obs.Tempo, tagQuery, 20)
		default:
			found = jaegerSearchByAttr(client, obs.Jaeger, tagQuery, 20)
		}
		if len(found) > 0 {
			ids = found
			return true
		}
		return false
	})
	return ids
}

// RequireSpanAttrs loads a trace and requires that at least one span carries
// every key/value in want (string form). Values are compared via Emit()-style
// formatting so int attributes match decimal strings.
func RequireSpanAttrs(t *testing.T, obs ObservabilityEndpoints, traceID string, want map[string]string) {
	t.Helper()
	require.NotEmpty(t, traceID)
	require.NotEmpty(t, want)
	client := &http.Client{Timeout: 10 * time.Second}

	var attrs []map[string]string
	var ok bool
	switch obs.Profile.TraceBackend() {
	case "tempo":
		attrs, ok = tempoTraceSpanAttrs(client, obs.Tempo, traceID)
	default:
		attrs, ok = jaegerTraceSpanAttrs(client, obs.Jaeger, traceID)
	}
	require.True(t, ok, "failed to load %s trace %s", obs.Profile.TraceBackend(), traceID)

	for _, spanAttrs := range attrs {
		if spanAttrsMatch(spanAttrs, want) {
			return
		}
	}
	t.Fatalf("trace %s: no span carries attrs %v (saw %d spans)", traceID, want, len(attrs))
}

// QueryPrometheusInstant runs an instant PromQL query against the observability
// Prometheus and returns metric label sets with a positive sample value.
func QueryPrometheusInstant(t *testing.T, obs ObservabilityEndpoints, query string) []map[string]string {
	t.Helper()
	series, ok := TryQueryPrometheusInstant(obs, query)
	require.True(t, ok, "prometheus query %q failed", query)
	return series
}

// TryQueryPrometheusInstant is the non-fatal form used while polling scrape lag.
func TryQueryPrometheusInstant(obs ObservabilityEndpoints, query string) ([]map[string]string, bool) {
	return queryPrometheusInstant(obs, query)
}

func queryPrometheusInstant(obs ObservabilityEndpoints, query string) ([]map[string]string, bool) {
	if query == "" {
		return nil, false
	}
	client := &http.Client{Timeout: 10 * time.Second}
	u, err := url.Parse(strings.TrimRight(obs.Prometheus, "/") + "/api/v1/query")
	if err != nil {
		return nil, false
	}
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, false
	}

	var parsed struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Status != "success" {
		return nil, false
	}
	out := make([]map[string]string, 0, len(parsed.Data.Result))
	for _, r := range parsed.Data.Result {
		if prometheusSamplePositive(r.Value) {
			out = append(out, r.Metric)
		}
	}
	return out, true
}

func prometheusSamplePositive(value []any) bool {
	if len(value) < 2 {
		return false
	}
	switch v := value[1].(type) {
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return err == nil && f > 0
	case float64:
		return v > 0
	default:
		return false
	}
}

func spanAttrsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func tempoSearchByAttr(client *http.Client, baseURL, tagQuery string, limit int) []string {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/search")
	if err != nil {
		return nil
	}
	q := u.Query()
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	// Explicit window — without start/end Tempo logs range_seconds=0 and can
	// miss recently ingested spans depending on config.
	end := time.Now()
	start := end.Add(-30 * time.Minute)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	if looksLikeTraceQL(tagQuery) {
		q.Set("q", tagQuery)
	} else {
		// Compat: logfmt tags= for callers that pass service.name=… style.
		q.Set("tags", tagQuery)
	}
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var parsed struct {
		Traces []struct {
			TraceID string `json:"traceID"`
		} `json:"traces"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed.Traces))
	for _, tr := range parsed.Traces {
		if tr.TraceID != "" {
			out = append(out, tr.TraceID)
		}
	}
	return out
}

func jaegerSearchByAttr(client *http.Client, baseURL, tagQuery string, limit int) []string {
	tags, err := attrQueryToJaegerTags(tagQuery)
	if err != nil || len(tags) == 0 {
		return nil
	}
	tagJSON, err := json.Marshal(tags)
	if err != nil {
		return nil
	}
	u, err := url.Parse(baseURL + "/jaeger/api/traces")
	if err != nil {
		return nil
	}
	q := u.Query()
	q.Set("service", "devshardctl")
	q.Set("tags", string(tagJSON))
	q.Set("lookback", "1h")
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var parsed struct {
		Data []struct {
			TraceID string `json:"traceID"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed.Data))
	for _, tr := range parsed.Data {
		if tr.TraceID != "" {
			out = append(out, tr.TraceID)
		}
	}
	return out
}

var (
	traceQLEq = regexp.MustCompile(`span\.([A-Za-z0-9_.]+)\s*=\s*"([^"]*)"`)
	logfmtEq  = regexp.MustCompile(`([A-Za-z0-9_.]+)=([^\s]+)`)
)

func looksLikeTraceQL(q string) bool {
	s := strings.TrimSpace(q)
	return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")
}

// attrQueryToJaegerTags converts a TraceQL-shaped filter or logfmt tags into a
// Jaeger tags map. Only equality predicates are supported.
func attrQueryToJaegerTags(tagQuery string) (map[string]string, error) {
	q := strings.TrimSpace(tagQuery)
	out := make(map[string]string)
	if looksLikeTraceQL(q) {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(q, "{"), "}"))
		for _, m := range traceQLEq.FindAllStringSubmatch(inner, -1) {
			out[m[1]] = m[2]
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no span.attr = \"value\" predicates in %q", tagQuery)
		}
		return out, nil
	}
	for _, m := range logfmtEq.FindAllStringSubmatch(q, -1) {
		out[m[1]] = strings.Trim(m[2], `"`)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty tag query %q", tagQuery)
	}
	return out, nil
}

type otlpAttrValue struct {
	StringValue string   `json:"stringValue"`
	IntValue    string   `json:"intValue"`
	BoolValue   *bool    `json:"boolValue"`
	DoubleValue *float64 `json:"doubleValue"`
}

func (v otlpAttrValue) asString() string {
	if v.StringValue != "" {
		return v.StringValue
	}
	if v.IntValue != "" {
		return v.IntValue
	}
	if v.BoolValue != nil {
		return strconv.FormatBool(*v.BoolValue)
	}
	if v.DoubleValue != nil {
		return strconv.FormatFloat(*v.DoubleValue, 'f', -1, 64)
	}
	return ""
}

func tempoTraceSpanAttrs(client *http.Client, baseURL, traceID string) ([]map[string]string, bool) {
	u := strings.TrimRight(baseURL, "/") + "/api/traces/" + url.PathEscape(traceID)
	resp, err := client.Get(u)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}

	var otlp struct {
		Batches []struct {
			ScopeSpans []struct {
				Spans []struct {
					Name       string `json:"name"`
					Attributes []struct {
						Key   string        `json:"key"`
						Value otlpAttrValue `json:"value"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(body, &otlp); err == nil && len(otlp.Batches) > 0 {
		var out []map[string]string
		for _, b := range otlp.Batches {
			for _, ss := range b.ScopeSpans {
				for _, sp := range ss.Spans {
					attrs := make(map[string]string)
					if sp.Name != "" {
						attrs["name"] = sp.Name
					}
					for _, a := range sp.Attributes {
						if a.Key == "" {
							continue
						}
						if s := a.Value.asString(); s != "" {
							attrs[a.Key] = s
						}
					}
					out = append(out, attrs)
				}
			}
		}
		return out, len(out) > 0
	}

	return jaegerTraceSpanAttrsFromBody(body)
}

func jaegerTraceSpanAttrs(client *http.Client, baseURL, traceID string) ([]map[string]string, bool) {
	u := baseURL + "/jaeger/api/traces/" + url.PathEscape(traceID)
	resp, err := client.Get(u)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	return jaegerTraceSpanAttrsFromBody(body)
}

func jaegerTraceSpanAttrsFromBody(body []byte) ([]map[string]string, bool) {
	var parsed struct {
		Data []struct {
			Spans []struct {
				OperationName string `json:"operationName"`
				Tags          []struct {
					Key   string `json:"key"`
					Type  string `json:"type"`
					Value any    `json:"value"`
				} `json:"tags"`
			} `json:"spans"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false
	}
	var out []map[string]string
	for _, tr := range parsed.Data {
		for _, sp := range tr.Spans {
			attrs := make(map[string]string)
			if sp.OperationName != "" {
				attrs["name"] = sp.OperationName
			}
			for _, tag := range sp.Tags {
				if tag.Key == "" {
					continue
				}
				attrs[tag.Key] = fmt.Sprint(tag.Value)
			}
			out = append(out, attrs)
		}
	}
	return out, len(out) > 0
}
