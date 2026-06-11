package mlnodeclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewHTTPClientDoesNotFollowRedirects(t *testing.T) {
	redirectTargetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHit = true
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	for name, client := range map[string]*http.Client{
		"HttpClientFactory": (&HttpClientFactory{}).NewHTTPClient(5 * time.Second),
		"MockClientFactory": NewMockClientFactory().NewHTTPClient(5 * time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := client.Get(srv.URL)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusFound {
				t.Fatalf("expected redirect status to be returned as-is, got %d", resp.StatusCode)
			}
			if redirectTargetHit {
				t.Fatal("client followed the redirect; ML node responses must not trigger requests to arbitrary URLs")
			}
		})
	}
}
