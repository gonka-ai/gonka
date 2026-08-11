package mlnode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestVersionsRequiresPublishedRuntimeConfig(t *testing.T) {
	tests := []struct {
		name   string
		server *Server
	}{
		{name: "missing config manager", server: NewServer(nil, nil)},
		{name: "unpublished config manager", server: NewServer(nil, nil, WithConfigManager(&apiconfig.ConfigManager{}))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tt.server.e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/versions", nil))
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		})
	}
}

func TestVersionsReturnsPublishedCatalogRevision(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		Versions: []apiconfig.DevshardVersion{{Name: "v5", Binary: "https://example.test/v5.zip"}},
	})
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(42, 7))
	server := NewServer(nil, nil, WithConfigManager(cm))

	recorder := httptest.NewRecorder()
	server.e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/versions", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	var response struct {
		Schema      int                         `json:"schema"`
		Initialized bool                        `json:"initialized"`
		Revision    int64                       `json:"revision"`
		Versions    []apiconfig.DevshardVersion `json:"versions"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, 1, response.Schema)
	require.True(t, response.Initialized)
	require.Equal(t, int64(42), response.Revision)
	require.Equal(t, []apiconfig.DevshardVersion{{Name: "v5", Binary: "https://example.test/v5.zip"}}, response.Versions)
}
