package utils_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/utils"
)

func TestSendPostJsonRequestWithAuth(t *testing.T) {
	// Setup test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify content type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("unexpected content type: %v", ct)
		}

		// Only verify auth header if test case provides auth token
		if r.URL.Query().Get("expectAuth") == "true" {
			if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
				t.Errorf("unexpected auth header: %v", auth)
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	// Test cases
	tests := []struct {
		name      string
		payload   interface{}
		authToken string
		wantErr   bool
	}{
		{
			name:      "valid request",
			payload:   map[string]string{"key": "value"},
			authToken: "test-token",
			wantErr:   false,
		},
		{
			name:      "empty auth token",
			payload:   map[string]string{"key": "value"},
			authToken: "",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := utils.SendPostJsonRequestWithAuth(
				context.Background(),
				ts.Client(),
				ts.URL,
				tt.payload,
				tt.authToken,
			)

			if (err != nil) != tt.wantErr {
				t.Errorf("SendPostJsonRequestWithAuth() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}
