package oracle

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConsensusVerifierRequiresCurrentConsensusArtifacts(t *testing.T) {
	shaA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	catchingUp := false
	consensusSHA := shaA
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"result":{"sync_info":{"catching_up":%t,"latest_block_height":"100"}}}`, catchingUp)
	})
	mux.HandleFunc("/params", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"params":{"devshard_escrow_params":{"approved_versions":[{"name":"v5","binary":"https://example.invalid/v5.zip","sha256":"%s"}]}}}`, consensusSHA)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	verifier := NewConsensusVerifier(server.URL+"/params", server.URL+"/status")
	candidate := catalogForTest(42, Version{
		Name: "v5", Binary: "https://example.invalid/v5.zip", SHA256: shaA,
	})
	if err := verifier.Verify(context.Background(), candidate); err != nil {
		t.Fatalf("Verify matching catalog: %v", err)
	}

	consensusSHA = shaB
	if err := verifier.Verify(context.Background(), candidate); err == nil {
		t.Fatal("Verify accepted stale DAPI artifact")
	}

	consensusSHA = shaA
	catchingUp = true
	if err := verifier.Verify(context.Background(), candidate); err == nil {
		t.Fatal("Verify accepted catalog while consensus node was catching up")
	}
}

func TestClientDoesNotReturnCatalogRejectedByConsensusVerifier(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mux := http.NewServeMux()
	mux.HandleFunc("/versions", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"schema":1,"initialized":true,"revision":42,"versions":[{"name":"v5","binary":"https://example.invalid/v5.zip","sha256":"%s"}]}`, sha)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"result":{"sync_info":{"catching_up":false,"latest_block_height":"100"}}}`)
	})
	mux.HandleFunc("/params", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"params":{"devshard_escrow_params":{"approved_versions":[]}}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient(server.URL+"/versions", WithCatalogVerifier(
		NewConsensusVerifier(server.URL+"/params", server.URL+"/status"),
	))
	if _, err := client.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch returned a catalog that did not match consensus")
	}
}

func TestConsensusVerifierRejectsMalformedSyncStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"result":{"sync_info":{}}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	verifier := NewConsensusVerifier(server.URL+"/params", server.URL+"/status")
	if err := verifier.Verify(context.Background(), catalogForTest(1)); err == nil {
		t.Fatal("Verify accepted a status response without catching_up")
	}
}

func TestConsensusVerifierRejectsRevisionAheadOfLocalConsensus(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"result":{"sync_info":{"catching_up":false,"latest_block_height":"41"}}}`)
	})
	mux.HandleFunc("/params", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"params":{"devshard_escrow_params":{"approved_versions":[{"name":"v5","binary":"https://example.invalid/v5.zip","sha256":"%s"}]}}}`, sha)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	verifier := NewConsensusVerifier(server.URL+"/params", server.URL+"/status")
	err := verifier.Verify(context.Background(), catalogForTest(42, Version{
		Name: "v5", Binary: "https://example.invalid/v5.zip", SHA256: sha,
	}))
	if err == nil {
		t.Fatal("Verify accepted a DAPI revision ahead of local consensus")
	}
}
