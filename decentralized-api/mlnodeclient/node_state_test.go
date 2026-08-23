package mlnodeclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNodeState_ParsesPocValidationInference(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/state" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"state":"INFERENCE","version":"0.2.0","poc_validation_inference":true}`))
	}))
	defer srv.Close()

	c := NewNodeClient(srv.URL, srv.URL)
	resp, err := c.NodeState(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !resp.PoCValidationInference {
		t.Fatalf("expected poc_validation_inference=true: %+v", resp)
	}
}

func TestNodeState_MissingPocValidationInferenceFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"INFERENCE","version":"0.2.0"}`))
	}))
	defer srv.Close()

	c := NewNodeClient(srv.URL, srv.URL)
	resp, err := c.NodeState(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.PoCValidationInference {
		t.Fatalf("expected missing poc_validation_inference to decode false: %+v", resp)
	}
}
