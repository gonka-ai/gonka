package quality

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCanonicalPromptHash_Deterministic(t *testing.T) {
	msgs := []map[string]string{
		{"role": "user", "content": "Review this function for null pointer safety"},
	}
	h1 := CanonicalPromptHash(msgs)
	h2 := CanonicalPromptHash(msgs)
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %s != %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("expected sha256 hex length 64, got %d", len(h1))
	}
}

func TestCanonicalPromptHash_DifferentContent(t *testing.T) {
	a := []map[string]string{{"role": "user", "content": "Hello"}}
	b := []map[string]string{{"role": "user", "content": "Hello!"}}
	if CanonicalPromptHash(a) == CanonicalPromptHash(b) {
		t.Fatal("different prompts should have different hashes")
	}
}

func TestMiddleware_CountsCompletion(t *testing.T) {
	qm := New(9700)
	handler := qm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	stats := qm.Stats()
	if stats.TotalRequests != 1 {
		t.Fatalf("expected 1 request, got %d", stats.TotalRequests)
	}
	if stats.CompletionRate != 1.0 {
		t.Fatalf("expected 100%% completion, got %f", stats.CompletionRate)
	}
}

func TestMiddleware_CountsFailure(t *testing.T) {
	qm := New(9700)
	handler := qm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"nil pointer"}`))
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	stats := qm.Stats()
	if stats.CompletionRate != 0.0 {
		t.Fatalf("expected 0%% completion on 500, got %f", stats.CompletionRate)
	}
}

func TestMiddleware_CacheHitCounting(t *testing.T) {
	qm := New(9700)
	handler := qm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cache", "HIT")
		w.Header().Set("X-Cache-Level", "1")
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	stats := qm.Stats()
	if stats.CacheHits != 1 {
		t.Fatalf("expected 1 cache hit, got %d", stats.CacheHits)
	}
	if stats.HitRate != 1.0 {
		t.Fatalf("expected 100%% hit rate, got %f", stats.HitRate)
	}
}

func TestMiddleware_FeedbackTracking(t *testing.T) {
	qm := New(9700)
	handler := qm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Inference-Feedback", `{"inference_id":"abc123","outcome":"resolved"}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	stats := qm.Stats()
	if stats.FeedbackResolved != 1 {
		t.Fatalf("expected 1 resolved feedback, got %d", stats.FeedbackResolved)
	}
}

func TestStatsHandler_JSON(t *testing.T) {
	qm := New(9700)
	handler := qm.StatsHandler()

	req := httptest.NewRequest("GET", "/quality/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
}

// ─── Block A: Mesh content exchange ─────────────────────────────────────────

func makeVec(seed float32, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = seed + float32(i)*0.001
	}
	return v
}

func TestMesh_ShareAndSearch_FullContent(t *testing.T) {
	qm := New(4250)

	vec := makeVec(0.5, 384)
	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID:   "agent-A",
		SlotID:   "slot-001",
		HitMode:  "slot",
		Quality:  0.85,
		Vec:      vec,
		Task:     "fix nil pointer in handler",
		Solution: "if req.Body != nil { defer req.Body.Close() }",
		Domain:   "go_nil_fix",
	})

	results := qm.MeshSearch(vec, 5)
	if len(results) == 0 {
		t.Fatal("expected at least 1 mesh result, got 0")
	}
	r := results[0]
	if r.Entry.Task != "fix nil pointer in handler" {
		t.Fatalf("expected task content, got %q", r.Entry.Task)
	}
	if r.Entry.Solution != "if req.Body != nil { defer req.Body.Close() }" {
		t.Fatalf("expected solution content, got %q", r.Entry.Solution)
	}
	if r.Entry.Domain != "go_nil_fix" {
		t.Fatalf("expected domain go_nil_fix, got %q", r.Entry.Domain)
	}
	if r.SimBps < MinSearchSimBps {
		t.Fatalf("expected simBps >= %d, got %d", MinSearchSimBps, r.SimBps)
	}
}

func TestMesh_SearchHandler_ReturnsContent(t *testing.T) {
	qm := New(4250)
	vec := makeVec(0.7, 384)
	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID: "agent-B", SlotID: "slot-002", HitMode: "slot",
		Quality: 0.9, Vec: vec,
		Task: "add mutex to cache", Solution: "sync.RWMutex for concurrent map",
		Domain: "go_concurrency",
	})

	body, _ := json.Marshal(map[string]any{"query": vec, "top_k": 5})
	req := httptest.NewRequest("POST", "/quality/search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	qm.SearchHandler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Results []struct {
			Task     string `json:"task"`
			Solution string `json:"solution"`
			Domain   string `json:"domain"`
			SimBps   uint32 `json:"sim_bps"`
		} `json:"results"`
		Count int `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count == 0 {
		t.Fatal("expected results, got 0")
	}
	if resp.Results[0].Task != "add mutex to cache" {
		t.Fatalf("expected task content in HTTP response, got %q", resp.Results[0].Task)
	}
	if resp.Results[0].Solution != "sync.RWMutex for concurrent map" {
		t.Fatalf("expected solution content, got %q", resp.Results[0].Solution)
	}
	if resp.Results[0].Domain != "go_concurrency" {
		t.Fatalf("expected domain, got %q", resp.Results[0].Domain)
	}
}

func TestMesh_ShareHandler_AcceptsContent(t *testing.T) {
	qm := New(4250)
	vec := makeVec(0.3, 384)
	payload, _ := json.Marshal(map[string]any{
		"node_id": "agent-C",
		"slots": []map[string]any{{
			"slot_id": "slot-003", "vec": vec, "hit_mode": "slot",
			"quality": 0.8, "task": "deploy to k8s",
			"solution": "kubectl apply -f deployment.yaml", "domain": "k8s_ops",
		}},
	})

	req := httptest.NewRequest("POST", "/quality/slots/share", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	qm.ShareHandler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	results := qm.MeshSearch(vec, 5)
	if len(results) == 0 {
		t.Fatal("share did not store content")
	}
	if results[0].Entry.Task != "deploy to k8s" {
		t.Fatalf("content not preserved through share→search, got %q", results[0].Entry.Task)
	}
	if results[0].Entry.Domain != "k8s_ops" {
		t.Fatalf("domain not preserved, got %q", results[0].Entry.Domain)
	}
}

// ─── Block B: Domain discovery ──────────────────────────────────────────────

func TestMesh_DomainsHandler(t *testing.T) {
	qm := New(4250)
	for i := 0; i < 3; i++ {
		qm.RecordMeshSignal(MeshPoolEntry{
			NodeID: fmt.Sprintf("node-%d", i), SlotID: fmt.Sprintf("s-%d", i),
			HitMode: "slot", Quality: 0.7, Vec: makeVec(float32(i)*0.1+0.1, 384),
			Task: "fix race condition", Domain: "go_race",
		})
	}
	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID: "node-X", SlotID: "s-X", HitMode: "slot", Quality: 0.9,
		Vec: makeVec(0.9, 384), Task: "deploy nginx", Domain: "k8s_deploy",
	})

	req := httptest.NewRequest("GET", "/quality/domains", nil)
	rec := httptest.NewRecorder()
	qm.DomainsHandler().ServeHTTP(rec, req)

	var resp struct {
		Domains []DomainInfo `json:"domains"`
		Total   int          `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Domains) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(resp.Domains))
	}
	if resp.Domains[0].Domain != "go_race" {
		t.Fatalf("expected go_race first (most slots), got %q", resp.Domains[0].Domain)
	}
	if resp.Domains[0].NodeCount != 3 {
		t.Fatalf("expected 3 nodes in go_race, got %d", resp.Domains[0].NodeCount)
	}
	if resp.Total != 4 {
		t.Fatalf("expected total 4, got %d", resp.Total)
	}
}

func TestMesh_DomainFilter_Search(t *testing.T) {
	qm := New(4250)
	vec := makeVec(0.5, 384)
	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID: "A", SlotID: "s1", HitMode: "slot", Quality: 0.8,
		Vec: vec, Task: "go task", Domain: "go_code",
	})
	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID: "B", SlotID: "s2", HitMode: "slot", Quality: 0.8,
		Vec: vec, Task: "k8s task", Domain: "k8s_ops",
	})

	body, _ := json.Marshal(map[string]any{"query": vec, "top_k": 10, "domain": "k8s_ops"})
	req := httptest.NewRequest("POST", "/quality/search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	qm.SearchHandler().ServeHTTP(rec, req)

	var resp struct {
		Results []struct{ Domain string } `json:"results"`
		Count   int                       `json:"count"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Count != 1 {
		t.Fatalf("domain filter: expected 1 result, got %d", resp.Count)
	}
	if resp.Results[0].Domain != "k8s_ops" {
		t.Fatalf("domain filter: expected k8s_ops, got %q", resp.Results[0].Domain)
	}
}

// ─── Block C: Fault tolerance ───────────────────────────────────────────────

func TestMesh_Concurrent_100Goroutines(t *testing.T) {
	qm := New(4250)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			qm.RecordMeshSignal(MeshPoolEntry{
				NodeID: fmt.Sprintf("n%d", idx), SlotID: fmt.Sprintf("s%d", idx),
				HitMode: "slot", Quality: 0.7, Vec: makeVec(float32(idx)*0.01, 384),
				Task: fmt.Sprintf("task %d", idx), Domain: "concurrent_test",
			})
		}(i)
		go func(idx int) {
			defer wg.Done()
			qm.MeshSearch(makeVec(float32(idx)*0.01, 384), 3)
		}(i)
	}
	wg.Wait()

	stats := qm.meshPoolStats()
	if stats == nil || stats.TotalSignals == 0 {
		t.Fatal("concurrent test: no signals recorded")
	}
}

func TestMesh_CorruptData_Rejected(t *testing.T) {
	qm := New(4250)

	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID: "bad", SlotID: "empty-vec", HitMode: "slot",
		Quality: 0.8, Vec: nil, Task: "should be ignored",
	})
	results := qm.MeshSearch(makeVec(0.5, 384), 5)
	for _, r := range results {
		if r.Entry.SlotID == "empty-vec" {
			t.Fatal("entry with nil Vec should not appear in search results")
		}
	}

	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID: "bad2", SlotID: "zero-vec", HitMode: "slot",
		Quality: 0.8, Vec: make([]float32, 384), Task: "zeros",
	})
	results = qm.MeshSearch(makeVec(0.5, 384), 5)
	for _, r := range results {
		if r.Entry.SlotID == "zero-vec" {
			t.Fatal("entry with zero Vec should not appear in search results")
		}
	}
}

func TestMesh_Eviction_BoundedPool(t *testing.T) {
	qm := New(4250)
	for i := 0; i < MaxPoolEntries+500; i++ {
		qm.RecordMeshSignal(MeshPoolEntry{
			NodeID: "evictor", SlotID: fmt.Sprintf("s%d", i),
			HitMode: "slot", Quality: 0.5,
			Vec: makeVec(float32(i)*0.0001, 384), Task: fmt.Sprintf("t%d", i),
		})
	}

	qm.meshMu.Lock()
	poolSize := len(qm.pool)
	qm.meshMu.Unlock()
	if poolSize > MaxPoolEntries {
		t.Fatalf("pool should be bounded at %d, got %d", MaxPoolEntries, poolSize)
	}
}

func TestMesh_QualityGate_BelowMinRejected(t *testing.T) {
	qm := New(4250)
	vec := makeVec(0.5, 384)
	qm.RecordMeshSignal(MeshPoolEntry{
		NodeID: "low-q", SlotID: "low", HitMode: "slot",
		Quality: 0.1, Vec: vec, Task: "low quality task",
	})
	results := qm.MeshSearch(vec, 5)
	for _, r := range results {
		if r.Entry.SlotID == "low" {
			t.Fatal("entry below MinPoolQuality should not appear in results")
		}
	}
}
