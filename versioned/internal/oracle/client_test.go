package oracle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	testSHA256A = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testSHA256B = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func TestFetch(t *testing.T) {
	want := VersionConfig{
		Versions: []Version{
			{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: testSHA256A},
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
			{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: testSHA256A},
			{Name: "", Binary: "http://example.com/empty.zip", SHA256: testSHA256B},
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

func TestValidVersionNameMatchesRoutingContract(t *testing.T) {
	for _, name := range []string{"v5", "v4+hotfix", "v4}x", "v9;hotfix"} {
		if !validVersionName(name) {
			t.Errorf("validVersionName(%q) = false", name)
		}
	}
	for _, name := range []string{"", ".", "..", " v5", "v 5", "v5/next", `v5\next`, "v5?x", "v5#x", "v5%x", `v5"x`, "v5'x", "v5\nnext"} {
		if validVersionName(name) {
			t.Errorf("validVersionName(%q) = true", name)
		}
	}
}

func TestFetch_RejectsDuplicateVersionNames(t *testing.T) {
	payload := VersionConfig{
		Versions: []Version{
			{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: testSHA256A},
			{Name: "v1", Binary: "http://example.com/v1-r2.zip", SHA256: testSHA256B},
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

func TestFetch_RejectsInvalidSHA256(t *testing.T) {
	payload := VersionConfig{
		Versions: []Version{
			{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: "../escape"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("expected invalid oracle sha256 to fail fetch")
	}
}

func TestResolvedSHA256_FromField(t *testing.T) {
	v := Version{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: testSHA256A}
	got, err := v.ResolvedSHA256()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testSHA256A {
		t.Errorf("got %q, want %q", got, testSHA256A)
	}
}

func TestResolvedSHA256_FromURL(t *testing.T) {
	v := Version{
		Name:   "v1",
		Binary: "http://example.com/v1.zip?checksum=sha256:" + testSHA256B,
	}
	got, err := v.ResolvedSHA256()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testSHA256B {
		t.Errorf("got %q, want %q", got, testSHA256B)
	}
}

func TestResolvedSHA256_FieldTakesPrecedence(t *testing.T) {
	v := Version{
		Name:   "v1",
		Binary: "http://example.com/v1.zip?checksum=sha256:" + testSHA256B,
		SHA256: testSHA256A,
	}
	got, err := v.ResolvedSHA256()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testSHA256A {
		t.Errorf("got %q, want %q", got, testSHA256A)
	}
}

func TestResolvedSHA256_InvalidField(t *testing.T) {
	v := Version{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: "../escape"}
	_, err := v.ResolvedSHA256()
	if err == nil {
		t.Fatal("expected error for invalid sha256 field")
	}
}

func TestResolvedSHA256_InvalidURLChecksum(t *testing.T) {
	v := Version{Name: "v1", Binary: "http://example.com/v1.zip?checksum=sha256:bad"}
	_, err := v.ResolvedSHA256()
	if err == nil {
		t.Fatal("expected error for invalid URL checksum")
	}
}

func TestResolvedSHA256_NoChecksum(t *testing.T) {
	v := Version{Name: "v1", Binary: "http://example.com/v1.zip"}
	_, err := v.ResolvedSHA256()
	if err == nil {
		t.Fatal("expected error when no checksum source")
	}
}
