package public

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"decentralized-api/poc/artifacts"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestGetPocArtifactsStateRejectsDotSegments(t *testing.T) {
	dir := t.TempDir()
	store := artifacts.NewManagedArtifactStore(dir, 3)
	defer store.Close()
	store.ActivateStage(100)
	legitimate, err := store.GetOrCreateStore(100, "org/model.v1")
	require.NoError(t, err)
	require.NoError(t, legitimate.AddWithNode(1, []byte("artifact"), "node-a"))
	require.NoError(t, legitimate.Flush())
	count, root := legitimate.GetFlushedRoot()
	store.ActivateStage(200)
	server := &Server{artifactStore: store}
	e := echo.New()
	e.GET("/v1/poc/artifacts/state", server.getPocArtifactsState)
	for _, modelQuery := range []string{".", "..", "%2e", "%2E%2e"} {
		for _, height := range []int64{100, 1000, 1001, 1002} {
			t.Run(fmt.Sprintf("%s/%d", modelQuery, height), func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/poc/artifacts/state?height=%d&model_id=%s", height, modelQuery), nil)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				require.Equal(t, http.StatusNotFound, rec.Code)
			})
		}
	}
	for _, parent := range []string{dir, filepath.Join(dir, "100")} {
		for _, filename := range []string{"artifacts.data", "distributions.jsonl", "flushed_roots.jsonl"} {
			_, err := os.Stat(filepath.Join(parent, filename))
			require.True(t, os.IsNotExist(err), "%s: %v", filepath.Join(parent, filename), err)
		}
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/poc/artifacts/state?height=100&model_id=org%2Fmodel.v1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var response PocArtifactsStateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Equal(t, count, response.Count)
	require.Equal(t, base64.StdEncoding.EncodeToString(root), response.RootHash)
	require.EqualValues(t, 200, store.ActiveStage())
}
