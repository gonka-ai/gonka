package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerReturnsLegacyArray(t *testing.T) {
	handler := Handler(func() []StatusEntry {
		return []StatusEntry{{Name: "v1", Port: 5000, Status: "running"}}
	})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	want := `[{"name":"v1","port":5000,"status":"running"}]`
	if got := strings.TrimSpace(response.Body.String()); got != want {
		t.Fatalf("body = %s, want legacy array %s", got, want)
	}
}
