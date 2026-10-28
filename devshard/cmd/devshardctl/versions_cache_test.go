package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func mlnodesHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/versions" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}
}

func TestVersionsCache_PollAndQuery(t *testing.T) {
	// miner A: node a1 capable, a2 not.
	srvA := httptest.NewServer(mlnodesHandler(`{"mlnodes":[{"node_id":"a1","poc_validation_inference":true},{"node_id":"a2","poc_validation_inference":false}]}`))
	defer srvA.Close()
	// miner B: 404 (old dapi).
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srvB.Close()

	c := NewVersionsCache(&http.Client{Timeout: 2 * time.Second}, time.Minute)
	c.SetCandidates(map[string]string{"A": srvA.URL, "B": srvB.URL})
	c.Poll(context.Background())

	if !c.IsNodeValidationCapable("A", "a1") {
		t.Fatal("A/a1 should be capable")
	}
	if c.IsNodeValidationCapable("A", "a2") {
		t.Fatal("A/a2 should not be capable")
	}
	if c.IsNodeValidationCapable("A", "unknownNode") {
		t.Fatal("unknown node on known miner -> false")
	}
	if c.IsNodeValidationCapable("B", "anything") {
		t.Fatal("B (404) must fail closed")
	}
	if c.IsNodeValidationCapable("never", "n") {
		t.Fatal("never-polled miner must fail closed")
	}
}

func TestVersionsCache_StaleFailsClosed(t *testing.T) {
	srv := httptest.NewServer(mlnodesHandler(`{"mlnodes":[{"node_id":"n1","poc_validation_inference":true}]}`))
	defer srv.Close()

	c := NewVersionsCache(&http.Client{Timeout: time.Second}, time.Nanosecond) // immediately stale
	c.SetCandidates(map[string]string{"m": srv.URL})
	c.Poll(context.Background())
	time.Sleep(time.Millisecond)
	if c.IsNodeValidationCapable("m", "n1") {
		t.Fatal("stale entry must fail closed")
	}
}

func TestVersionsCache_OversizedResponseFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write([]byte(`{"mlnodes":[{"node_id":"n1","poc_validation_inference":true}`))
		entry := []byte(`,{"node_id":"n1","poc_validation_inference":true}`)
		for written := 0; written <= maxVersionsResponseBytes; written += len(entry) {
			gz.Write(entry)
		}
		gz.Write([]byte(`]}`))
		gz.Close()
	}))
	defer srv.Close()

	c := NewVersionsCache(&http.Client{Timeout: 2 * time.Second}, time.Minute)
	c.SetCandidates(map[string]string{"m": srv.URL})
	c.Poll(context.Background())
	if c.IsNodeValidationCapable("m", "n1") {
		t.Fatal("oversized response must fail closed")
	}
}

func TestVersionsCache_OversizedErrorResponseNotDrained(t *testing.T) {
	const offered = 64 << 20
	var written int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		chunk := bytes.Repeat([]byte("e"), 64<<10)
		for int(atomic.LoadInt64(&written)) < offered {
			n, err := w.Write(chunk)
			atomic.AddInt64(&written, int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := NewVersionsCache(&http.Client{Timeout: 10 * time.Second}, time.Minute)
	c.SetCandidates(map[string]string{"m": srv.URL})
	c.Poll(context.Background())

	if c.IsNodeValidationCapable("m", "n1") {
		t.Fatal("error response must fail closed")
	}
	if got := atomic.LoadInt64(&written); got >= offered {
		t.Fatalf("error body was drained without limit: %d bytes consumed", got)
	}
}

func TestVersionsCache_KeepsLastAnswerOnFetchError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "limited", http.StatusServiceUnavailable)
			return
		}
		mlnodesHandler(`{"mlnodes":[{"node_id":"n1","poc_validation_inference":true}]}`)(w, r)
	}))
	defer srv.Close()

	now := time.Unix(1_000, 0)
	c := NewVersionsCache(&http.Client{Timeout: time.Second}, 10*time.Minute)
	c.now = func() time.Time { return now }
	c.SetCandidates(map[string]string{"m": srv.URL})
	c.Poll(context.Background())
	if !c.IsNodeValidationCapable("m", "n1") {
		t.Fatal("first fetch should mark n1 capable")
	}

	fail.Store(true)
	now = now.Add(6 * time.Minute)
	c.Poll(context.Background())
	if !c.IsNodeValidationCapable("m", "n1") {
		t.Fatal("a failed refresh must keep the last good answer until ttl")
	}

	now = now.Add(5 * time.Minute)
	if c.IsNodeValidationCapable("m", "n1") {
		t.Fatal("answer older than ttl must fail closed")
	}
}

func TestVersionsCache_PollFetchesOnlyDueCandidates(t *testing.T) {
	var hits atomic.Int64
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		mlnodesHandler(`{"mlnodes":[{"node_id":"n1","poc_validation_inference":true}]}`)(w, r)
	}))
	defer srv.Close()

	now := time.Unix(1_000, 0)
	c := NewVersionsCache(&http.Client{Timeout: time.Second}, 10*time.Minute)
	c.now = func() time.Time { return now }
	c.SetCandidates(map[string]string{"m": srv.URL})

	c.Poll(context.Background())
	now = now.Add(versionsRefreshInterval / 2)
	c.Poll(context.Background())
	if got := hits.Load(); got != 1 {
		t.Fatalf("fresh answer must not be refetched, hits=%d", got)
	}

	now = now.Add(versionsRefreshInterval / 2)
	fail.Store(true)
	c.Poll(context.Background())
	if got := hits.Load(); got != 2 {
		t.Fatalf("answer past refreshAfter must be refetched, hits=%d", got)
	}

	now = now.Add(versionsRetryInterval / 2)
	c.Poll(context.Background())
	if got := hits.Load(); got != 2 {
		t.Fatalf("failed fetch must wait retryAfter, hits=%d", got)
	}

	now = now.Add(versionsRetryInterval)
	c.Poll(context.Background())
	if got := hits.Load(); got != 3 {
		t.Fatalf("failed fetch must be retried after retryAfter, hits=%d", got)
	}
}

func TestVersionsCache_RunPollsOnlyWhileActive(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mlnodesHandler(`{"mlnodes":[{"node_id":"n1","poc_validation_inference":true}]}`)(w, r)
	}))
	defer srv.Close()

	c := NewVersionsCache(&http.Client{Timeout: time.Second}, 10*time.Minute)
	c.SetCandidates(map[string]string{"m": srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, 5*time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("inactive cache must not fetch, hits=%d", got)
	}

	c.SetActive(true)
	deadline := time.Now().Add(2 * time.Second)
	for !c.IsNodeValidationCapable("m", "n1") {
		if time.Now().After(deadline) {
			t.Fatal("activation must trigger a fetch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("active cache must not refetch a fresh answer, hits=%d", got)
	}
}

func TestVersionsCache_PollDoesNotResurrectRemovedCandidate(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		mlnodesHandler(`{"mlnodes":[{"node_id":"n1","poc_validation_inference":true}]}`)(w, r)
	}))
	defer srv.Close()

	c := NewVersionsCache(&http.Client{Timeout: 2 * time.Second}, 10*time.Minute)
	c.SetCandidates(map[string]string{"m": srv.URL})
	done := make(chan struct{})
	go func() {
		c.Poll(context.Background())
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	c.SetCandidates(map[string]string{})
	close(release)
	<-done

	c.mu.RLock()
	_, ok := c.entries["m"]
	c.mu.RUnlock()
	if ok {
		t.Fatal("entry for a miner dropped mid-poll must not be stored")
	}
}

func TestVersionsCache_URLChangeInvalidatesEntry(t *testing.T) {
	old := httptest.NewServer(mlnodesHandler(`{"mlnodes":[{"node_id":"n","poc_validation_inference":true}]}`))
	defer old.Close()
	var hits atomic.Int64
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mlnodesHandler(`{"mlnodes":[{"node_id":"n","poc_validation_inference":false}]}`)(w, r)
	}))
	defer next.Close()

	c := NewVersionsCache(&http.Client{Timeout: time.Second}, 10*time.Minute)
	c.SetCandidates(map[string]string{"m": old.URL})
	c.Poll(context.Background())
	if !c.IsNodeValidationCapable("m", "n") {
		t.Fatal("old endpoint answer should be cached")
	}

	c.SetCandidates(map[string]string{"m": next.URL})
	if c.IsNodeValidationCapable("m", "n") {
		t.Fatal("URL change must drop the old endpoint's answer")
	}
	c.Poll(context.Background())
	if got := hits.Load(); got != 1 {
		t.Fatalf("new endpoint must be fetched, hits=%d", got)
	}
	if c.IsNodeValidationCapable("m", "n") {
		t.Fatal("new endpoint reports n not capable")
	}
}

func TestVersionsCache_InFlightAnswerForReplacedURLIsDropped(t *testing.T) {
	release := make(chan struct{})
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		mlnodesHandler(`{"mlnodes":[{"node_id":"n","poc_validation_inference":true}]}`)(w, r)
	}))
	defer old.Close()

	c := NewVersionsCache(&http.Client{Timeout: 2 * time.Second}, 10*time.Minute)
	c.SetCandidates(map[string]string{"m": old.URL})
	done := make(chan struct{})
	go func() {
		c.Poll(context.Background())
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	c.SetCandidates(map[string]string{"m": "http://replacement.example"})
	close(release)
	<-done

	if c.IsNodeValidationCapable("m", "n") {
		t.Fatal("answer fetched from the replaced URL must not be stored")
	}
}
