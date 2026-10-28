package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/internal/testutil"
)

// A host's error page reaches an error string, a metric label and a log line, so its size is the
// host's choice unless the read is bounded.
func TestAHugeErrorPageIsTruncatedBeforeItBecomesALabel(t *testing.T) {
	oversized := strings.Repeat("x", maxErrorBodyBytes*3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, oversized, http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)

	client := NewHTTPClient(upstream.URL, "escrow-1", testutil.MustGenerateKey(t))

	_, err := client.doPost(t.Context(), "/sessions/escrow-1/verify-timeout", []byte(`{}`))

	var status *UpstreamStatusError
	if !errors.As(err, &status) {
		t.Fatalf("err = %v, want an UpstreamStatusError", err)
	}
	if len(status.Body) != maxErrorBodyBytes {
		t.Fatalf("kept %d bytes of a %d-byte error page, want the %d-byte cap",
			len(status.Body), len(oversized), maxErrorBodyBytes)
	}
}
