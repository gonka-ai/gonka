package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassicInferenceRoutesReturnDeprecated(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "post chat completions",
			method: http.MethodPost,
			path:   "/v1/chat/completions",
			body:   `{"model":"test","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:   "get chat completions",
			method: http.MethodGet,
			path:   "/v1/chat/completions?id=abc",
		},
		{
			name:   "post completions",
			method: http.MethodPost,
			path:   "/v1/completions",
			body:   `{"model":"test","prompt":"hi"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()

			s.e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusGone, rec.Code)
			require.Equal(t, "true", rec.Header().Get("Deprecation"))
			require.Contains(t, rec.Header().Get("Link"), "/devshard/{version}")

			var body map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, "deprecated", body["error"])
			require.Contains(t, body["message"], "classic inference is deprecated")
			require.Contains(t, body["message"], "devshard")
		})
	}
}
