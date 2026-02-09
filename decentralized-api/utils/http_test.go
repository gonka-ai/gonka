package utils_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/utils"
)

func TestSendPostJsonRequestWithAuth(t *testing.T) {
	// Test cases
	tests := []struct {
		name           string
		payload        interface{}
		authToken      string
		expectedAuth   string
		wantErr        bool
	}{
		{
			name:         "valid request with auth token",
			payload:      map[string]string{"key": "value"},
			authToken:    "test-token",
			expectedAuth: "Bearer test-token",
			wantErr:      false,
		},
		{
			name:         "empty auth token should not send header",
			payload:      map[string]string{"key": "value"},
			authToken:    "",
			expectedAuth: "",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test server for each test case
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify content type
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("unexpected content type: %v", ct)
				}

				// Verify auth header matches expected
				auth := r.Header.Get("Authorization")
				if auth != tt.expectedAuth {
					t.Errorf("unexpected auth header: got %q, want %q", auth, tt.expectedAuth)
				}

				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			}))
			defer ts.Close()

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
