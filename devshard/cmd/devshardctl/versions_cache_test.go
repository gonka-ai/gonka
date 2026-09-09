package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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
