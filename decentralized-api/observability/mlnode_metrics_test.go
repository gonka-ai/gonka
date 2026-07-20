package observability

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Same metric name from multiple mlnodes must merge into one valid,
// re-parseable exposition with node_id labels and correct up state.
func TestMLNodeMetricsHandler_MergesLabelsAndUp(t *testing.T) {
	body := func(val string) string {
		return "# HELP vllm_num_requests_running Running requests.\n" +
			"# TYPE vllm_num_requests_running gauge\n" +
			"vllm_num_requests_running " + val + "\n"
	}
	nodeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body("3")))
	}))
	defer nodeA.Close()
	nodeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body("7")))
	}))
	defer nodeB.Close()

	// unreachable node: closed server refuses connections fast
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	downURL := down.URL
	down.Close()

	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{
			{ID: "node-a", URL: nodeA.URL},
			{ID: "node-b", URL: nodeB.URL},
			{ID: "node-down", URL: downURL},
		}, nil
	}

	h := MLNodeMetricsHandler(list, MLNodeMetricsConfig{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/mlnodes/metrics", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := parser.TextToMetricFamilies(strings.NewReader(rr.Body.String()))
	if err != nil {
		t.Fatalf("merged output is not valid Prometheus exposition: %v\n---\n%s", err, rr.Body.String())
	}

	fam, ok := fams["vllm_num_requests_running"]
	if !ok {
		t.Fatalf("merged output missing vllm_num_requests_running; got families: %v keys", len(fams))
	}
	if len(fam.Metric) != 2 {
		t.Fatalf("vllm_num_requests_running series = %d, want 2 (one per reachable node)", len(fam.Metric))
	}
	byNode := map[string]float64{}
	for _, m := range fam.Metric {
		var nodeID string
		for _, l := range m.Label {
			if l.GetName() == "node_id" {
				nodeID = l.GetValue()
			}
		}
		if nodeID == "" {
			t.Fatalf("series without node_id label: %v", m)
		}
		byNode[nodeID] = m.GetGauge().GetValue()
	}
	if byNode["node-a"] != 3 || byNode["node-b"] != 7 {
		t.Fatalf("values by node = %v, want node-a=3 node-b=7", byNode)
	}

	up, ok := fams["mlnode_up"]
	if !ok {
		t.Fatalf("merged output missing mlnode_up")
	}
	upByNode := map[string]float64{}
	for _, m := range up.Metric {
		for _, l := range m.Label {
			if l.GetName() == "node_id" {
				upByNode[l.GetValue()] = m.GetGauge().GetValue()
			}
		}
	}
	if upByNode["node-a"] != 1 || upByNode["node-b"] != 1 || upByNode["node-down"] != 0 {
		t.Fatalf("mlnode_up = %v, want node-a=1 node-b=1 node-down=0", upByNode)
	}
}

// A 404 node (metrics off / pre-metrics image) is absent entirely: no
// series, no mlnode_up.
func TestMLNodeMetricsHandler_NotExposedNodeIsAbsent(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE m gauge\nm 1\n"))
	}))
	defer up.Close()
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer off.Close()

	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{
			{ID: "node-on", URL: up.URL},
			{ID: "node-off", URL: off.URL},
		}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rr.Body.String()
	if strings.Contains(body, "node-off") {
		t.Fatalf("404-node must be absent from the output entirely, got:\n%s", body)
	}
	if !strings.Contains(body, `mlnode_up{node_id="node-on"} 1`) {
		t.Fatalf("reachable node missing from mlnode_up:\n%s", body)
	}
}

// A node over the per-node series cap is rejected and reported down.
func TestMLNodeMetricsHandler_SeriesCapRejectsNode(t *testing.T) {
	var b strings.Builder
	b.WriteString("# TYPE flood gauge\n")
	for i := 0; i < maxSeriesPerNode+1; i++ {
		fmt.Fprintf(&b, "flood{i=\"%d\"} 1\n", i)
	}
	flood := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	}))
	defer flood.Close()

	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "node-flood", URL: flood.URL}}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rr.Body.String()
	if strings.Contains(body, "flood{") {
		t.Fatalf("over-cap node's series must be rejected, got some in output")
	}
	if !strings.Contains(body, `mlnode_up{node_id="node-flood"} 0`) {
		t.Fatalf("over-cap node must be reported down:\n%s", body)
	}
}

// A fleet where every node answers 404 (all on pre-metrics images — the
// day-one state of a dapi-first rollout) must produce an empty, valid 200
// exposition, not an error.
func TestMLNodeMetricsHandler_AllNodesNotExposed(t *testing.T) {
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer off.Close()

	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "a", URL: off.URL}, {ID: "b", URL: off.URL}}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "" {
		t.Fatalf("want empty exposition, got:\n%s", rr.Body.String())
	}
}

// An empty inventory must likewise yield an empty 200 exposition.
func TestMLNodeMetricsHandler_EmptyTargetList(t *testing.T) {
	list := func() ([]MLNodeTarget, error) { return nil, nil }
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "" {
		t.Fatalf("want empty exposition, got:\n%s", rr.Body.String())
	}
}

// Realistic exporter payload: colon-namespaced vLLM families and a histogram
// (bucket/sum/count) from two nodes must merge into one re-parseable family
// with per-node labels intact.
func TestMLNodeMetricsHandler_RealisticFamiliesMerge(t *testing.T) {
	body := func(waiting string) string {
		return "# HELP vllm:num_requests_waiting Waiting.\n" +
			"# TYPE vllm:num_requests_waiting gauge\n" +
			"vllm:num_requests_waiting{model_name=\"m\",replica=\"0\"} " + waiting + "\n" +
			"# HELP vllm:time_to_first_token_seconds TTFT.\n" +
			"# TYPE vllm:time_to_first_token_seconds histogram\n" +
			"vllm:time_to_first_token_seconds_bucket{model_name=\"m\",replica=\"0\",le=\"0.5\"} 1\n" +
			"vllm:time_to_first_token_seconds_bucket{model_name=\"m\",replica=\"0\",le=\"+Inf\"} 2\n" +
			"vllm:time_to_first_token_seconds_sum{model_name=\"m\",replica=\"0\"} 0.7\n" +
			"vllm:time_to_first_token_seconds_count{model_name=\"m\",replica=\"0\"} 2\n"
	}
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body("3")))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body("7")))
	}))
	defer b.Close()

	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "node-a", URL: a.URL}, {ID: "node-b", URL: b.URL}}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := parser.TextToMetricFamilies(strings.NewReader(rr.Body.String()))
	if err != nil {
		t.Fatalf("merged output is not valid exposition: %v\n%s", err, rr.Body.String())
	}
	hist, ok := fams["vllm:time_to_first_token_seconds"]
	if !ok || len(hist.Metric) != 2 {
		t.Fatalf("histogram family: ok=%v metrics=%d, want 2 (one per node)", ok, len(hist.Metric))
	}
	if hist.Metric[0].GetHistogram() == nil {
		t.Fatalf("histogram type lost in merge")
	}
}

// A node whose exposition exceeds the byte ceiling is rejected and reported
// down instead of ballooning the merged snapshot.
func TestMLNodeMetricsHandler_BodyCapRejectsNode(t *testing.T) {
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE big gauge\n"))
		filler := strings.Repeat("x", 63)
		for i := 0; i*64 < maxBodyBytes+4096; i++ {
			fmt.Fprintf(w, "# %s\n", filler)
		}
		_, _ = w.Write([]byte("big 1\n"))
	}))
	defer huge.Close()

	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "node-huge", URL: huge.URL}}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	if strings.Contains(body, "big 1") {
		t.Fatalf("over-cap node's series must be rejected")
	}
	if !strings.Contains(body, `mlnode_up{node_id="node-huge"} 0`) {
		t.Fatalf("over-cap node must be reported down:\n%s", body)
	}
}

// An OpenMetrics Accept header gets an OpenMetrics response terminated by the
// # EOF marker (truncation detection), while the default stays classic text.
func TestMLNodeMetricsHandler_NegotiatesOpenMetrics(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE m gauge\nm 1\n"))
	}))
	defer node.Close()
	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "n", URL: node.URL}}, nil
	}
	h := MLNodeMetricsHandler(list, MLNodeMetricsConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Header().Get("Content-Type"), "openmetrics") {
		t.Fatalf("want openmetrics content type, got %q", rr.Header().Get("Content-Type"))
	}
	if !strings.HasSuffix(strings.TrimSpace(rr.Body.String()), "# EOF") {
		t.Fatalf("openmetrics response must end with # EOF:\n%s", rr.Body.String())
	}

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(rr2.Header().Get("Content-Type"), "openmetrics") {
		t.Fatalf("default must remain classic text")
	}
	if strings.Contains(rr2.Body.String(), "# EOF") {
		t.Fatalf("classic text must not carry # EOF")
	}
}

// A client that disconnects mid-fan-out must not poison the shared cache
// with an all-down snapshot: the rebuild is detached from the requester's
// context, so a subsequent request within the TTL sees every node up.
func TestMLNodeMetricsHandler_AbortedClientDoesNotPoisonCache(t *testing.T) {
	slow := func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("# TYPE m gauge\nm 1\n"))
	}
	a := httptest.NewServer(http.HandlerFunc(slow))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(slow))
	defer b.Close()

	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "node-a", URL: a.URL}, {ID: "node-b", URL: b.URL}}, nil
	}
	h := MLNodeMetricsHandler(list, MLNodeMetricsConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr2.Body.String()
	for _, node := range []string{"node-a", "node-b"} {
		if !strings.Contains(body, `mlnode_up{node_id="`+node+`"} 1`) {
			t.Fatalf("aborted first request poisoned the cache; second response:\n%s", body)
		}
	}
}

// A node emitting its own family named mlnode_up must not collide with the
// aggregator's reserved family (duplicate TYPE blocks are invalid).
func TestMLNodeMetricsHandler_ReservedUpFamilyIgnored(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE mlnode_up gauge\nmlnode_up{fake=\"1\"} 0\n# TYPE m gauge\nm 1\n"))
	}))
	defer node.Close()
	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "n", URL: node.URL}}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	if strings.Count(body, "# TYPE mlnode_up") != 1 {
		t.Fatalf("duplicate mlnode_up TYPE blocks:\n%s", body)
	}
	if strings.Contains(body, `fake="1"`) {
		t.Fatalf("node-supplied mlnode_up series must be dropped:\n%s", body)
	}
	if !strings.Contains(body, `mlnode_up{node_id="n"} 1`) {
		t.Fatalf("aggregator's own up series missing:\n%s", body)
	}
}

// A node smuggling its own node_id label must not produce duplicate label
// names (which invalidate the entire merged exposition).
func TestMLNodeMetricsHandler_NodeSuppliedNodeIDLabelStripped(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE m gauge\nm{node_id=\"evil\"} 1\n"))
	}))
	defer node.Close()
	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{{ID: "real", URL: node.URL}}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	if strings.Contains(body, `node_id="evil"`) {
		t.Fatalf("node-supplied node_id must be stripped:\n%s", body)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	if _, err := parser.TextToMetricFamilies(strings.NewReader(body)); err != nil {
		t.Fatalf("merged output must stay parseable: %v\n%s", err, body)
	}
	if !strings.Contains(body, `m{node_id="real"} 1`) {
		t.Fatalf("series lost its aggregator-owned node_id:\n%s", body)
	}
}

// Two nodes disagreeing on a family's type must not merge into one invalid
// family; the later node's family is dropped.
func TestMLNodeMetricsHandler_TypeMismatchDropped(t *testing.T) {
	gaugeNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE m gauge\nm 1\n"))
	}))
	defer gaugeNode.Close()
	counterNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE m counter\nm 2\n"))
	}))
	defer counterNode.Close()
	list := func() ([]MLNodeTarget, error) {
		return []MLNodeTarget{
			{ID: "a", URL: gaugeNode.URL}, {ID: "b", URL: counterNode.URL}}, nil
	}
	rr := httptest.NewRecorder()
	MLNodeMetricsHandler(list, MLNodeMetricsConfig{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	if strings.Count(body, "\nm{") != 1 {
		t.Fatalf("exactly one node's family must survive a type mismatch:\n%s", body)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	if _, err := parser.TextToMetricFamilies(strings.NewReader(body)); err != nil {
		t.Fatalf("merged output must stay parseable: %v\n%s", err, body)
	}
}
