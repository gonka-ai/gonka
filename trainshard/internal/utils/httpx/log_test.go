package httpx_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"trainshard/internal/utils/httpx"
	"trainshard/internal/utils/timex"
)

func TestLogLeavesTheConnectionDeadlinesReachable(t *testing.T) {
	// arrange
	var set error
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		set = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Second))
	})
	server := httptest.NewServer(httpx.Log(slog.New(slog.DiscardHandler), timex.NewFrozen(time.Now()))(handler))
	defer server.Close()

	// act
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = response.Body.Close()

	// assert
	if set != nil {
		t.Fatalf("got %v, want the logged writer to hand the connection's read deadline through", set)
	}
}
