package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// GatewayAttemptSpanName is the gateway per-attempt span (observability.SpanNameGatewayAttempt).
const GatewayAttemptSpanName = "gateway.attempt"

// Stage names emitted by mock-dapi on the node-selection hop.
const (
	StageMLNodeAcquire = "mlnode_acquire"
	StageMLNodeRelease = "mlnode_release"
)

// PostGatewayChatSoftEx posts a chat and returns the status plus response
// headers without failing the test. The multi-host attempt citest drives
// several chats while hunting for one that fanned out to two hosts; a round the
// gateway refuses is a round to skip, not a test failure.
func PostGatewayChatSoftEx(
	t *testing.T,
	client *http.Client,
	gatewayURL, adminAPIKey string,
	req ChatCompletionRequest,
) (status int, header http.Header, content string) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		t.Logf("citest: chat transport error: %v", err)
		return 0, http.Header{}, ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("citest: chat body read error: %v", err)
		return resp.StatusCode, resp.Header.Clone(), ""
	}
	if resp.StatusCode >= 300 {
		t.Logf("citest: chat status=%d body=%s", resp.StatusCode, truncateForLog(string(body)))
		return resp.StatusCode, resp.Header.Clone(), ""
	}
	var out ChatCompletionResponse
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		t.Logf("citest: chat 200 but unusable body: %v %s", err, truncateForLog(string(body)))
		return resp.StatusCode, resp.Header.Clone(), ""
	}
	return resp.StatusCode, resp.Header.Clone(), out.Choices[0].Message.Content
}

func truncateForLog(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// LokiEntry is one JSON log line plus the stream labels Loki attached to it.
type LokiEntry struct {
	ComposeService string
	Line           string
	Fields         map[string]any
}

// Str returns a string field, or "" when absent or non-string.
func (e LokiEntry) Str(key string) string {
	s, _ := e.Fields[key].(string)
	return s
}

// TryQueryLokiEntries runs a LogQL range query and parses each line as JSON.
// Lines that are not JSON are skipped; callers always query with `| json`.
func TryQueryLokiEntries(obs ObservabilityEndpoints, query string, limit int) ([]LokiEntry, bool) {
	if limit <= 0 {
		limit = 200
	}
	client := &http.Client{Timeout: 10 * time.Second}
	end := time.Now()
	start := end.Add(-15 * time.Minute)
	u, err := url.Parse(strings.TrimRight(obs.Loki, "/") + "/loki/api/v1/query_range")
	if err != nil {
		return nil, false
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
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
	var parsed struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false
	}
	var out []LokiEntry
	for _, stream := range parsed.Data.Result {
		for _, v := range stream.Values {
			if len(v) < 2 {
				continue
			}
			fields := make(map[string]any)
			if err := json.Unmarshal([]byte(v[1]), &fields); err != nil {
				continue
			}
			out = append(out, LokiEntry{
				ComposeService: stream.Stream["compose_service"],
				Line:           v[1],
				Fields:         fields,
			})
		}
	}
	return out, true
}

// WaitLokiEntries polls until the query returns at least minEntries parsed lines.
func WaitLokiEntries(t *testing.T, obs ObservabilityEndpoints, query string, minEntries int, timeout time.Duration) []LokiEntry {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if minEntries <= 0 {
		minEntries = 1
	}
	t.Logf("citest: waiting for >=%d Loki entries: %s", minEntries, query)

	var entries []LokiEntry
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		got, queried := TryQueryLokiEntries(obs, query, 500)
		if !queried || len(got) < minEntries {
			return false
		}
		entries = got
		return true
	})
	require.True(t, ok, "Loki returned fewer than %d entries within %s for %s", minEntries, timeout, query)
	return entries
}

// RequireRequestIDOnTrace asserts that, for each compose_service, at least one
// line on traceID also carries the client's request_id. This is the join that
// proves gateway, host and dapi logs describe one user request rather than
// three coincidental ones on the same trace.
func RequireRequestIDOnTrace(
	t *testing.T,
	obs ObservabilityEndpoints,
	traceID, requestID string,
	composeServices []string,
	timeout time.Duration,
) {
	t.Helper()
	require.NotEmpty(t, traceID)
	require.NotEmpty(t, requestID, "gateway must return X-Request-Id")
	require.NotEmpty(t, composeServices)

	for _, svc := range composeServices {
		query := fmt.Sprintf(`{compose_service=~%q} | json | trace_id=%q | request_id=%q`, svc, traceID, requestID)
		t.Logf("citest: waiting for request_id=%s on trace_id=%s service=~%s", requestID, traceID, svc)
		entries := WaitLokiEntries(t, obs, query, 1, timeout)
		t.Logf("citest: %s has %d line(s) with request_id=%s", svc, len(entries), requestID)
	}
}

// RequireStagesForTrace asserts each stage appears on composeService for traceID.
func RequireStagesForTrace(
	t *testing.T,
	obs ObservabilityEndpoints,
	traceID, composeService string,
	stages []string,
	timeout time.Duration,
) []LokiEntry {
	t.Helper()
	require.NotEmpty(t, traceID)
	require.NotEmpty(t, stages)

	var all []LokiEntry
	for _, stage := range stages {
		query := fmt.Sprintf(`{compose_service=~%q} | json | trace_id=%q | stage=%q`,
			composeService, traceID, stage)
		all = append(all, WaitLokiEntries(t, obs, query, 1, timeout)...)
	}
	return all
}

// TraceIDsForRequest returns the distinct trace_ids stamped on composeService
// lines carrying requestID.
func TraceIDsForRequest(t *testing.T, obs ObservabilityEndpoints, composeService, requestID string, timeout time.Duration) []string {
	t.Helper()
	ids, ok := TryTraceIDsForRequest(obs, composeService, requestID, timeout)
	require.True(t, ok, "no %s Loki line with request_id=%s within %s", composeService, requestID, timeout)
	return ids
}

// TryTraceIDsForRequest is TraceIDsForRequest without the assertion, for
// callers that drive several requests and discard the uninteresting ones.
func TryTraceIDsForRequest(
	obs ObservabilityEndpoints,
	composeService, requestID string,
	timeout time.Duration,
) ([]string, bool) {
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	query := fmt.Sprintf(`{compose_service=~%q} | json | request_id=%q`, composeService, requestID)

	deadline := time.Now().Add(timeout)
	for {
		entries, queried := TryQueryLokiEntries(obs, query, 500)
		if queried {
			seen := make(map[string]struct{})
			for _, e := range entries {
				if id := e.Str("trace_id"); id != "" {
					seen[id] = struct{}{}
				}
			}
			if len(seen) > 0 {
				out := make([]string, 0, len(seen))
				for id := range seen {
					out = append(out, id)
				}
				sort.Strings(out)
				return out, true
			}
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		time.Sleep(3 * time.Second)
	}
}

// RequireSingleTraceForRequest asserts one client request produced exactly one
// trace on composeService and returns it. A second root trace here means some
// hop minted its own trace instead of continuing the caller's.
func RequireSingleTraceForRequest(t *testing.T, obs ObservabilityEndpoints, composeService, requestID string, timeout time.Duration) string {
	t.Helper()
	ids := TraceIDsForRequest(t, obs, composeService, requestID, timeout)
	require.Len(t, ids, 1, "request_id=%s should map to exactly one trace_id on %s, got %v",
		requestID, composeService, ids)
	return ids[0]
}

// WaitTraceServices polls until traceID reports every service.name in want.
// Unlike WaitTraceCoveringServices this pins the assertion to one trace, so a
// stray earlier request cannot satisfy it.
func WaitTraceServices(t *testing.T, obs ObservabilityEndpoints, traceID string, want []string, timeout time.Duration) {
	t.Helper()
	require.NotEmpty(t, traceID)
	require.NotEmpty(t, want)
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	client := &http.Client{Timeout: 10 * time.Second}
	t.Logf("citest: waiting for trace %s to cover services %v", traceID, want)

	var have map[string]struct{}
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		services, loaded := traceServicesForID(client, obs, traceID)
		if !loaded {
			return false
		}
		have = services
		for _, svc := range want {
			if _, found := services[svc]; !found {
				return false
			}
		}
		return true
	})
	require.True(t, ok, "trace %s does not cover %v within %s (saw %v)",
		traceID, want, timeout, sortedKeys(have))
}

func traceServicesForID(client *http.Client, obs ObservabilityEndpoints, traceID string) (map[string]struct{}, bool) {
	if obs.Profile.TraceBackend() == "tempo" {
		return tempoTraceServices(client, obs.Tempo, traceID)
	}
	return jaegerTraceServices(client, obs.Jaeger, traceID)
}

func jaegerTraceServices(client *http.Client, baseURL, traceID string) (map[string]struct{}, bool) {
	resp, err := client.Get(baseURL + "/jaeger/api/traces/" + url.PathEscape(traceID))
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
	var parsed struct {
		Data []struct {
			Processes map[string]struct {
				ServiceName string `json:"serviceName"`
			} `json:"processes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false
	}
	services := make(map[string]struct{})
	for _, tr := range parsed.Data {
		for _, p := range tr.Processes {
			if p.ServiceName != "" {
				services[p.ServiceName] = struct{}{}
			}
		}
	}
	return services, len(services) > 0
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TraceSpans returns one attribute map per span in traceID; the span name is
// under the "name" key.
func TraceSpans(obs ObservabilityEndpoints, traceID string) ([]map[string]string, bool) {
	client := &http.Client{Timeout: 10 * time.Second}
	if obs.Profile.TraceBackend() == "tempo" {
		return tempoTraceSpanAttrs(client, obs.Tempo, traceID)
	}
	return jaegerTraceSpanAttrs(client, obs.Jaeger, traceID)
}

// WaitTraceSpanNames polls until traceID carries every span name in want, then
// returns all spans. Spans arrive in batches, so a trace that is missing a
// child right now may have it a second later.
func WaitTraceSpanNames(t *testing.T, obs ObservabilityEndpoints, traceID string, want []string, timeout time.Duration) []map[string]string {
	t.Helper()
	require.NotEmpty(t, traceID)
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	var spans []map[string]string
	var missing []string
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		got, loaded := TraceSpans(obs, traceID)
		if !loaded {
			return false
		}
		spans = got
		missing = missingSpanNames(got, want)
		return len(missing) == 0
	})
	require.True(t, ok, "trace %s missing spans %v within %s (saw %v)",
		traceID, missing, timeout, spanNames(spans))
	return spans
}

// WaitTraceSpanNameAny is WaitTraceSpanNames for a set where any one name is
// enough — e.g. the acquire hop may be proven by the host client span or the
// dapi server span.
func WaitTraceSpanNameAny(t *testing.T, obs ObservabilityEndpoints, traceID string, anyOf []string, timeout time.Duration) string {
	t.Helper()
	require.NotEmpty(t, anyOf)
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	var found string
	var seen []string
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		spans, loaded := TraceSpans(obs, traceID)
		if !loaded {
			return false
		}
		seen = spanNames(spans)
		for _, want := range anyOf {
			if len(missingSpanNames(spans, []string{want})) == 0 {
				found = want
				return true
			}
		}
		return false
	})
	require.True(t, ok, "trace %s has none of %v within %s (saw %v)", traceID, anyOf, timeout, seen)
	return found
}

// GatewayAttemptParticipants returns the attempt-span count and the distinct
// participant keys those attempts targeted.
func GatewayAttemptParticipants(spans []map[string]string) (int, []string) {
	attempts := 0
	seen := make(map[string]struct{})
	for _, s := range spans {
		if s["name"] != GatewayAttemptSpanName {
			continue
		}
		attempts++
		key := s["participant.key"]
		if key == "" {
			key = s["devshard.host.id"]
		}
		if key != "" {
			seen[key] = struct{}{}
		}
	}
	participants := make([]string, 0, len(seen))
	for key := range seen {
		participants = append(participants, key)
	}
	sort.Strings(participants)
	return attempts, participants
}

// TryWaitMultiHostAttempts polls traceID until its gateway.attempt spans cover
// at least minParticipants distinct hosts. Non-fatal by design: which host the
// picker chooses as primary is not fixed, so callers drive another chat instead
// of failing on the first single-host request.
func TryWaitMultiHostAttempts(
	obs ObservabilityEndpoints,
	traceID string,
	minParticipants int,
	timeout time.Duration,
) (attempts int, participants []string, ok bool) {
	if minParticipants <= 0 {
		minParticipants = 2
	}
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		spans, loaded := TraceSpans(obs, traceID)
		if loaded {
			attempts, participants = GatewayAttemptParticipants(spans)
			if len(participants) >= minParticipants {
				return attempts, participants, true
			}
		}
		if time.Now().After(deadline) {
			return attempts, participants, false
		}
		time.Sleep(3 * time.Second)
	}
}

func spanNames(spans []map[string]string) []string {
	seen := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if name := s["name"]; name != "" {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func missingSpanNames(spans []map[string]string, want []string) []string {
	have := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		have[s["name"]] = struct{}{}
	}
	var missing []string
	for _, name := range want {
		if _, ok := have[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}
