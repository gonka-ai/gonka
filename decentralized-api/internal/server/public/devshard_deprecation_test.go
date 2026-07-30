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
	// Non-obs /v1/devshard paths stay Gone. Obs paths are mounted only when
	// WithDevshardObs is set (see TestNewServerDevshardObsOverridesDeprecation).
	for _, path := range []string{
		"/v1/devshard",
		"/v1/devshard/chat/completions",
		"/v1/devshard/some-legacy",
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

func TestNewServerDevshardObsOverridesDeprecation(t *testing.T) {
	obs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("obs:" + r.URL.Path))
	})
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil, WithDevshardObs(obs))

	req := httptest.NewRequest(http.MethodGet, "/v1/devshard/stats/shards", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "obs:/v1/devshard/stats/shards", rec.Body.String())

	req2 := httptest.NewRequest(http.MethodGet, "/v1/devshard/chat/completions", nil)
	rec2 := httptest.NewRecorder()
	s.e.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusGone, rec2.Code)
}
