package mlnodeclient

import (
	"context"
	"decentralized-api/utils"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points a Client at srv for both the PoC and inference URLs.
func newTestClient(srv *httptest.Server) *Client {
	return NewNodeClient(srv.URL, srv.URL)
}

// TestClient_InferenceUpChecksStatus covers the false-positive that mattered
// most: the MLnode answers 409 when vLLM is already running or starting, and the
// old implementation discarded the response and reported success. The broker
// then treated a failed launch as healthy instead of retrying it.
func TestClient_InferenceUpChecksStatus(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantErr     bool
		wantRetry   bool
		wantMessage string
	}{
		{name: "ok", status: http.StatusOK, body: `{"status":"OK"}`},
		{
			name:        "already running",
			status:      http.StatusConflict,
			body:        `{"detail":"VLLM is already running."}`,
			wantErr:     true,
			wantRetry:   true,
			wantMessage: "already running",
		},
		{
			name:      "startup failed",
			status:    http.StatusInternalServerError,
			body:      `{"detail":"Failed to start VLLM"}`,
			wantErr:   true,
			wantRetry: true,
		},
		{
			name:      "validation error",
			status:    http.StatusUnprocessableEntity,
			body:      `{"detail":"bad request"}`,
			wantErr:   true,
			wantRetry: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != inferenceUpPath {
					t.Errorf("path = %s, want %s", r.URL.Path, inferenceUpPath)
				}
				// FastAPI cannot parse a JSON body without the content type.
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			err := newTestClient(srv).InferenceUp(context.Background(), "model-a", []string{"--flag"})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("status %d: expected an error", tc.status)
				}
				if got := IsTransientStatus(err); got != tc.wantRetry {
					t.Errorf("IsTransientStatus = %v, want %v (err=%v)", got, tc.wantRetry, err)
				}
				if tc.wantMessage != "" && !strings.Contains(err.Error(), tc.wantMessage) {
					t.Errorf("error %q does not mention %q", err, tc.wantMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestClient_StopChecksStatus guards the same class of bug on /stop: the MLnode
// only answers 200, so any other status means the node did not stop. Reporting
// success there let the broker's redeploy and the pre-PoC test proceed against a
// node still running a model.
func TestClient_StopChecksStatus(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != stopPath {
				t.Errorf("path = %s, want %s", r.URL.Path, stopPath)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		if err := newTestClient(srv).Stop(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"detail":"could not stop"}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).Stop(context.Background())
		if err == nil {
			t.Fatal("a non-200 /stop must be reported as a failure")
		}
		var statusErr *StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("error %v is not a StatusError", err)
		}
		if statusErr.StatusCode != http.StatusInternalServerError {
			t.Errorf("StatusCode = %d, want 500", statusErr.StatusCode)
		}
	})
}

// TestClient_InferenceBoundsResponseBody guards the probe request added for the
// pre-PoC test: an unbounded io.ReadAll would let a misconfigured or hostile
// endpoint make the API process buffer an arbitrary amount of data.
func TestClient_InferenceBoundsResponseBody(t *testing.T) {
	t.Run("valid response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
		}))
		defer srv.Close()

		if err := newTestClient(srv).Inference(context.Background(), "model-a"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("oversized response is truncated, not buffered whole", func(t *testing.T) {
		// Far larger than the read cap. A truncated body is invalid JSON, so the
		// call fails — the point is that it fails rather than reading it all.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"`)
			chunk := strings.Repeat("x", 64<<10)
			for written := 0; written < maxInferenceBodyBytes+(1<<20); written += len(chunk) {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
			_, _ = io.WriteString(w, `"}}]}`)
		}))
		defer srv.Close()

		if err := newTestClient(srv).Inference(context.Background(), "model-a"); err == nil {
			t.Fatal("expected the truncated oversized response to fail parsing")
		}
	})

	t.Run("no choices is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}))
		defer srv.Close()

		if err := newTestClient(srv).Inference(context.Background(), "model-a"); err == nil {
			t.Fatal("a response with no choices must be an error")
		}
	})

	t.Run("non-200 is a transient StatusError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "backend down")
		}))
		defer srv.Close()

		err := newTestClient(srv).Inference(context.Background(), "model-a")
		if !IsStatusError(err) {
			t.Fatalf("error %v is not a StatusError", err)
		}
		if !IsTransientStatus(err) {
			t.Errorf("503 should be transient: %v", err)
		}
	})
}

// TestSendPostJsonRequest_ContentType documents the header change's actual
// blast radius. SendPostJsonRequest is shared: every caller with a non-nil
// payload now declares application/json, while a nil payload still sends no
// body and no header. This is not fixing a live failure — FastAPI falls back to
// JSON parsing when no content-type is present — it makes the request honest and
// keeps it working under strict content-type checking.
func TestSendPostJsonRequest_ContentType(t *testing.T) {
	t.Run("payload declares application/json", func(t *testing.T) {
		got := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got <- r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := &http.Client{}
		resp, err := utils.SendPostJsonRequest(context.Background(), client, srv.URL, map[string]string{"k": "v"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if ct := <-got; ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
	})

	t.Run("nil payload sends no content type", func(t *testing.T) {
		got := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got <- r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := &http.Client{}
		resp, err := utils.SendPostJsonRequest(context.Background(), client, srv.URL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if ct := <-got; ct != "" {
			t.Errorf("Content-Type = %q, want empty for a bodyless POST", ct)
		}
	})

	t.Run("unmarshalable payload returns an error, never (nil, nil)", func(t *testing.T) {
		// Guards the shadowed-err fix: a marshalling failure used to be
		// swallowed, and the function could return no response and no error —
		// leaving the caller to dereference a nil response.
		client := &http.Client{}
		resp, err := utils.SendPostJsonRequest(context.Background(), client, "http://127.0.0.1:1", func() {})
		if err == nil {
			t.Fatal("expected an error for an unmarshalable payload")
		}
		if resp != nil {
			t.Errorf("expected no response alongside the error, got %v", resp)
			resp.Body.Close()
		}
	})
}

// TestStatusErrorClassification pins the transient/permanent split callers use
// to decide whether retrying makes sense.
func TestStatusErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{status: http.StatusConflict, want: true},        // vLLM already running
		{status: http.StatusTooManyRequests, want: true}, //
		{status: http.StatusRequestTimeout, want: true},  //
		{status: http.StatusInternalServerError, want: true},
		{status: http.StatusBadGateway, want: true},
		{status: http.StatusBadRequest, want: false},
		{status: http.StatusNotFound, want: false},
		{status: http.StatusUnprocessableEntity, want: false},
	}
	for _, tc := range cases {
		err := &StatusError{Op: "op", StatusCode: tc.status}
		if got := err.Transient(); got != tc.want {
			t.Errorf("status %d: Transient() = %v, want %v", tc.status, got, tc.want)
		}
		if !IsStatusError(err) {
			t.Errorf("status %d: IsStatusError should be true", tc.status)
		}
		if got := IsTransientStatus(err); got != tc.want {
			t.Errorf("status %d: IsTransientStatus = %v, want %v", tc.status, got, tc.want)
		}
	}

	// A plain error carries no status, so it is neither.
	plain := errors.New("connection refused")
	if IsStatusError(plain) || IsTransientStatus(plain) {
		t.Error("a non-status error must not be classified as a StatusError")
	}
}
