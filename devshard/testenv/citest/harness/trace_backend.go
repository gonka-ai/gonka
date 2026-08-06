package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// WaitTraceSpan polls the active trace backend until a span with the given
// service + operation exists (Tempo or Jaeger depending on profile).
func WaitTraceSpan(t *testing.T, obs ObservabilityEndpoints, service, operation string, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	client := &http.Client{Timeout: 10 * time.Second}
	backend := obs.Profile.TraceBackend()
	t.Logf("citest: waiting for %s span service=%q operation=%q", backend, service, operation)
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		switch backend {
		case "tempo":
			return tempoHasOperation(client, obs.Tempo, service, operation)
		default:
			return jaegerHasOperation(client, obs.Jaeger, service, operation)
		}
	})
	require.True(t, ok, "%s span %q for service %q not found within %s",
		backend, operation, service, timeout)
}

// WaitJaegerSpan is a compatibility alias for WaitTraceSpan.
func WaitJaegerSpan(t *testing.T, obs ObservabilityEndpoints, service, operation string, timeout time.Duration) {
	t.Helper()
	WaitTraceSpan(t, obs, service, operation, timeout)
}

// WaitTraceCoveringServices polls until one trace includes every listed service.name.
func WaitTraceCoveringServices(t *testing.T, obs ObservabilityEndpoints, services []string, timeout time.Duration) string {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	require.NotEmpty(t, services)
	client := &http.Client{Timeout: 10 * time.Second}
	backend := obs.Profile.TraceBackend()
	t.Logf("citest: waiting for %s trace covering services %v", backend, services)

	var traceID string
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		var id string
		var found bool
		switch backend {
		case "tempo":
			id, found = tempoTraceCoveringServices(client, obs.Tempo, services)
		default:
			id, found = jaegerTraceCoveringServices(client, obs.Jaeger, services)
		}
		if found {
			traceID = id
		}
		return found
	})
	require.True(t, ok, "%s trace covering %v not found within %s", backend, services, timeout)
	require.NotEmpty(t, traceID)
	return traceID
}

// WaitJaegerTraceWithServices is a compatibility alias for WaitTraceCoveringServices.
func WaitJaegerTraceWithServices(t *testing.T, obs ObservabilityEndpoints, services []string, timeout time.Duration) string {
	t.Helper()
	return WaitTraceCoveringServices(t, obs, services, timeout)
}

func jaegerHasOperation(client *http.Client, baseURL, service, operation string) bool {
	u, err := url.Parse(baseURL + "/jaeger/api/traces")
	if err != nil {
		return false
	}
	q := u.Query()
	q.Set("service", service)
	q.Set("operation", operation)
	q.Set("limit", "20")
	q.Set("lookback", "1h")
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	if strings.Contains(string(body), operation) {
		return true
	}
	var parsed struct {
		Data []struct {
			Spans []struct {
				OperationName string `json:"operationName"`
			} `json:"spans"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	for _, trace := range parsed.Data {
		for _, span := range trace.Spans {
			if span.OperationName == operation {
				return true
			}
		}
	}
	return false
}

func tempoHasOperation(client *http.Client, baseURL, service, operation string) bool {
	ids := tempoSearchTraceIDs(client, baseURL, map[string]string{
		"service.name": service,
		"name":         operation,
	}, 20)
	for _, id := range ids {
		if tempoTraceHasOperation(client, baseURL, id, service, operation) {
			return true
		}
	}
	// Fallback: search by service only, then inspect spans (some Tempo builds
	// ignore the name tag on /api/search).
	ids = tempoSearchTraceIDs(client, baseURL, map[string]string{
		"service.name": service,
	}, 20)
	for _, id := range ids {
		if tempoTraceHasOperation(client, baseURL, id, service, operation) {
			return true
		}
	}
	return false
}

func tempoTraceCoveringServices(client *http.Client, baseURL string, wantServices []string) (string, bool) {
	ids := tempoSearchTraceIDs(client, baseURL, map[string]string{
		"service.name": wantServices[0],
	}, 20)
	for _, id := range ids {
		have, ok := tempoTraceServices(client, baseURL, id)
		if !ok {
			continue
		}
		all := true
		for _, svc := range wantServices {
			if _, ok := have[svc]; !ok {
				all = false
				break
			}
		}
		if all {
			return id, true
		}
	}
	return "", false
}

func tempoSearchTraceIDs(client *http.Client, baseURL string, tags map[string]string, limit int) []string {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/search")
	if err != nil {
		return nil
	}
	q := u.Query()
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var tagParts []string
	for k, v := range tags {
		tagParts = append(tagParts, fmt.Sprintf("%s=%s", k, v))
	}
	if len(tagParts) > 0 {
		q.Set("tags", strings.Join(tagParts, " "))
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

func tempoTraceHasOperation(client *http.Client, baseURL, traceID, service, operation string) bool {
	services, ops, ok := tempoTraceDetail(client, baseURL, traceID)
	if !ok {
		return false
	}
	if service != "" {
		if _, ok := services[service]; !ok {
			return false
		}
	}
	_, ok = ops[operation]
	return ok
}

func tempoTraceServices(client *http.Client, baseURL, traceID string) (map[string]struct{}, bool) {
	services, _, ok := tempoTraceDetail(client, baseURL, traceID)
	return services, ok
}

func tempoTraceDetail(client *http.Client, baseURL, traceID string) (map[string]struct{}, map[string]struct{}, bool) {
	u := strings.TrimRight(baseURL, "/") + "/api/traces/" + url.PathEscape(traceID)
	resp, err := client.Get(u)
	if err != nil {
		return nil, nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, false
	}

	// Tempo returns OTLP-ish JSON: batches[].resource.attributes + scopeSpans[].spans[].
	var otlp struct {
		Batches []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					Name string `json:"name"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	services := make(map[string]struct{})
	ops := make(map[string]struct{})
	if err := json.Unmarshal(body, &otlp); err == nil && len(otlp.Batches) > 0 {
		for _, b := range otlp.Batches {
			for _, a := range b.Resource.Attributes {
				if a.Key == "service.name" && a.Value.StringValue != "" {
					services[a.Value.StringValue] = struct{}{}
				}
			}
			for _, ss := range b.ScopeSpans {
				for _, sp := range ss.Spans {
					if sp.Name != "" {
						ops[sp.Name] = struct{}{}
					}
				}
			}
		}
		return services, ops, len(services) > 0 || len(ops) > 0
	}

	// Fallback: Jaeger-shaped payload some Tempo versions expose.
	var jaegerShaped struct {
		Batches []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
		} `json:"batches"`
		Data []struct {
			Processes map[string]struct {
				ServiceName string `json:"serviceName"`
			} `json:"processes"`
			Spans []struct {
				OperationName string `json:"operationName"`
			} `json:"spans"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &jaegerShaped); err != nil {
		return nil, nil, false
	}
	for _, tr := range jaegerShaped.Data {
		for _, p := range tr.Processes {
			if p.ServiceName != "" {
				services[p.ServiceName] = struct{}{}
			}
		}
		for _, sp := range tr.Spans {
			if sp.OperationName != "" {
				ops[sp.OperationName] = struct{}{}
			}
		}
	}
	return services, ops, len(services) > 0 || len(ops) > 0
}

func lokiQueryContains(client *http.Client, baseURL, query, substring string) bool {
	end := time.Now()
	start := end.Add(-15 * time.Minute)
	u, err := url.Parse(baseURL + "/loki/api/v1/query_range")
	if err != nil {
		return false
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("limit", "50")
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), substring)
}
