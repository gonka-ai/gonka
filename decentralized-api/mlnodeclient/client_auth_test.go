package mlnodeclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/mlnode"
)

// A client built for a BaseURL+token node must (a) probe health at /readyz and
// (b) carry the bearer token on its requests.
func TestCreateClientForNode_BaseURLAuthAndReadyz(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ep := mlnode.New(mlnode.Spec{BaseURL: srv.URL, AuthToken: "tok"})
	client := (&HttpClientFactory{}).CreateClientForNode(ep, "")

	ok, err := client.InferenceHealth(context.Background())
	if err != nil {
		t.Fatalf("InferenceHealth: %v", err)
	}
	if !ok {
		t.Fatal("InferenceHealth returned not ok")
	}
	if gotPath != "/readyz" {
		t.Errorf("health path = %q, want /readyz", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
}

// A Host-Port client with no token keeps the legacy behavior: health at /health,
// no Authorization header.
func TestCreateClientForNode_HostPortNoAuthUsesHealth(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Use the server URL as the inference base by registering it as base_url is
	// not desired here; instead drive InferenceHealth on a host-port client whose
	// inference URL points at the test server.
	client := NewNodeClient(srv.URL, srv.URL)

	if _, err := client.InferenceHealth(context.Background()); err != nil {
		t.Fatalf("InferenceHealth: %v", err)
	}
	if gotPath != "/health" {
		t.Errorf("health path = %q, want /health", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}
