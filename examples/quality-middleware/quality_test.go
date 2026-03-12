package quality

import (
	"net/http"
	"net/http/httptest"
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
