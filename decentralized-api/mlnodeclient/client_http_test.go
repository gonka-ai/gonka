package mlnodeclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStop_RejectsNon2xx(t *testing.T) {
	t.Parallel()

	cases := []int{http.StatusNotFound, http.StatusConflict, http.StatusInternalServerError}
	for _, status := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/stop" {
					http.Error(w, "bad path", http.StatusNotFound)
					return
				}
				http.Error(w, "stop rejected", status)
			}))
			t.Cleanup(srv.Close)

			err := NewNodeClient(srv.URL, srv.URL).Stop(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "HTTP") {
				t.Fatalf("expected HTTP status in error, got %v", err)
			}
		})
	}
}

func TestInferenceUp_RejectsNon2xx(t *testing.T) {
	t.Parallel()

	cases := []int{http.StatusConflict, http.StatusInternalServerError}
	for _, status := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/inference/up" {
					http.Error(w, "bad path", http.StatusNotFound)
					return
				}
				io.Copy(io.Discard, r.Body)
				http.Error(w, "start rejected", status)
			}))
			t.Cleanup(srv.Close)

			err := NewNodeClient(srv.URL, srv.URL).InferenceUp(context.Background(), "model", nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "HTTP") {
				t.Fatalf("expected HTTP status in error, got %v", err)
			}
		})
	}
}

func TestStopAndInferenceUp_SucceedOn2xxAndCloseBody(t *testing.T) {
	t.Parallel()

	var stopRequests, upRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/api/v1/stop":
			stopRequests++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"OK"}`))
		case "/api/v1/inference/up":
			upRequests++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"OK"}`))
		default:
			http.Error(w, "bad path", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := NewNodeClient(srv.URL, srv.URL)
	if err := client.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := client.InferenceUp(context.Background(), "model", []string{"--served-model-name", "model"}); err != nil {
		t.Fatalf("inference up: %v", err)
	}
	if stopRequests != 1 || upRequests != 1 {
		t.Fatalf("expected one stop and one up, got stop=%d up=%d", stopRequests, upRequests)
	}
}
