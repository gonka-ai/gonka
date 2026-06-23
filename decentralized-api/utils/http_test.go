package utils

import (
	"net/http"
	"testing"
)

func TestSetBearerAuth_SetsHeaderWhenTokenPresent(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)

	SetBearerAuth(req, "tok")

	if got, want := req.Header.Get(AuthorizationHeader), "Bearer tok"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestSetBearerAuth_NoHeaderWhenEmpty(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)

	SetBearerAuth(req, "")

	if got := req.Header.Get(AuthorizationHeader); got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}
}
