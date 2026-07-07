package oracle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetch(t *testing.T) {
	want := VersionConfig{
		Versions: []Version{
			{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: "abc123"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Versions) != 1 {
		t.Fatalf("got %d versions, want 1", len(got.Versions))
	}
	if got.Versions[0].Name != "v1" {
		t.Errorf("name = %q, want %q", got.Versions[0].Name, "v1")
	}
}

func TestFetch_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestFetch_RejectsInvalidVersionNames(t *testing.T) {
	payload := VersionConfig{
		Versions: []Version{
			{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: "abc123"},
			{Name: "", Binary: "http://example.com/empty.zip", SHA256: "def456"},
			{Name: " v2 ", Binary: "http://example.com/spaces.zip", SHA256: "bad"},
			{Name: "../escape", Binary: "http://example.com/escape.zip", SHA256: "bad"},
			{Name: "nested/v2", Binary: "http://example.com/nested.zip", SHA256: "bad"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("expected invalid oracle version name to fail fetch")
	}
}

func TestFetch_RejectsDuplicateVersionNames(t *testing.T) {
	payload := VersionConfig{
		Versions: []Version{
			{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: "abc123"},
			{Name: "v1", Binary: "http://example.com/v1-r2.zip", SHA256: "def456"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("expected duplicate oracle version name to fail fetch")
	}
}

func TestResolvedSHA256_FromField(t *testing.T) {
	v := Version{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: "abc123"}
	got, err := v.ResolvedSHA256()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

func TestResolvedSHA256_FromURL(t *testing.T) {
	v := Version{
		Name:   "v1",
		Binary: "http://example.com/v1.zip?checksum=sha256:def456",
	}
	got, err := v.ResolvedSHA256()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "def456" {
		t.Errorf("got %q, want %q", got, "def456")
	}
}

func TestResolvedSHA256_FieldTakesPrecedence(t *testing.T) {
	v := Version{
		Name:   "v1",
		Binary: "http://example.com/v1.zip?checksum=sha256:url_hash",
		SHA256: "field_hash",
	}
	got, err := v.ResolvedSHA256()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "field_hash" {
		t.Errorf("got %q, want %q", got, "field_hash")
	}
}

func TestResolvedSHA256_NoChecksum(t *testing.T) {
	v := Version{Name: "v1", Binary: "http://example.com/v1.zip"}
	_, err := v.ResolvedSHA256()
	if err == nil {
		t.Fatal("expected error when no checksum source")
	}
}
