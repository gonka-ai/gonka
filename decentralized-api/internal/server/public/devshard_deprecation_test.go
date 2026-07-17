package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServerRegistersDeprecatedDevshardPaths(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)
	for _, path := range []string{
		"/v1/devshard",
		"/v1/devshard/stats/shards",
		"/v1/devshard/sessions/escrow-1/mempool",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusGone, rec.Code)
			require.Equal(t, "true", rec.Header().Get("Deprecation"))
			require.Contains(t, rec.Header().Get("Link"), "/devshard/{version}")

			var body map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, "deprecated", body["error"])
			require.Contains(t, body["message"], "/devshard/{version}")
		})
	}
}
