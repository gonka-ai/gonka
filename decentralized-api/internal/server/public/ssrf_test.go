package public

import (
	"bytes"
	"decentralized-api/utils"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultClientFollowsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"secret":"LEAKED"}`))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	req, _ := http.NewRequest(http.MethodPost, redirector.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	req.Header.Set(utils.XInferenceIdHeader, "test-id")
	req.Header.Set(utils.AuthorizationHeader, "Bearer key")
	req.Header.Set("Content-Type", "application/json")

	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !bytes.Contains(body, []byte("LEAKED")) {
		t.Error("DefaultClient follows redirects")
	}
}

func TestNoRedirectClient(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect was followed")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	req, _ := http.NewRequest(http.MethodPost, redirector.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	req.Header.Set(utils.XInferenceIdHeader, "test-id")
	req.Header.Set(utils.AuthorizationHeader, "Bearer key")
	req.Header.Set("Content-Type", "application/json")

	resp, err := NewNoRedirectClient(0).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("expected 307, got %d", resp.StatusCode)
	}
}
