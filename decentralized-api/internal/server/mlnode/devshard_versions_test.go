package mlnode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestVersionsServesLocalReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("api: {}\n"), 0600))
	cm, err := apiconfig.LoadConfigManagerWithPaths(path, filepath.Join(dir, "config.db"), "")
	require.NoError(t, err)
	o := apiconfig.DevshardVersionOverride{
		From: apiconfig.DevshardBinary{Binary: "https://example.com/a", SHA256: strings.Repeat("a", 64)},
		To:   apiconfig.DevshardBinary{Binary: "https://example.com/b", SHA256: strings.Repeat("b", 64)},
	}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{Versions: []apiconfig.DevshardVersion{{Name: "v1", Binary: o.From.Binary, SHA256: o.From.SHA256}}})
	s := NewServer(nil, nil, WithConfigManager(cm))
	fetch := func() apiconfig.DevshardVersion {
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/versions", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var result apiconfig.DevshardVersionsCache
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.Len(t, result.Versions, 1)
		return result.Versions[0]
	}
	require.Equal(t, o.From.Binary, fetch().Binary)
	require.NoError(t, cm.SetDevshardVersionOverride(context.Background(), o))
	require.Equal(t, apiconfig.DevshardVersion{Name: "v1", Binary: o.To.Binary, SHA256: o.To.SHA256}, fetch())
	require.NoError(t, cm.DeleteDevshardVersionOverride(context.Background(), o.From))
	require.Equal(t, o.From.Binary, fetch().Binary)
}
