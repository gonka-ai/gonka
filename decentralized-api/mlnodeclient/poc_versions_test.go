package mlnodeclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetPocVersions_ParsesFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/versions" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"vllm_version":"0.1.2","poc_validation_inference":true}`))
	}))
	defer srv.Close()

	c := NewNodeClient(srv.URL, srv.URL)
	resp, err := c.GetPocVersions(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !resp.PoCValidationInference || resp.VllmVersion != "0.1.2" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
}

func TestGetPocVersions_404IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewNodeClient(srv.URL, srv.URL)
	if _, err := c.GetPocVersions(context.Background()); err == nil {
		t.Fatal("expected error on 404")
	}
}
