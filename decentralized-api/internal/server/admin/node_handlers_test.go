package admin

import (
	"bytes"
	"decentralized-api/apiconfig"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestLaterDuplicateBatchIndex(t *testing.T) {
	nodes := []apiconfig.InferenceNodeConfig{
		{Id: "a"},
		{Id: "b"},
		{Id: "a"},
		{Id: "b"},
		{Id: "c"},
	}

	_, dup := laterDuplicateBatchIndex(nodes, 0)
	require.False(t, dup)
	_, dup = laterDuplicateBatchIndex(nodes, 1)
	require.False(t, dup)

	first, dup := laterDuplicateBatchIndex(nodes, 2)
	require.True(t, dup)
	require.Equal(t, 0, first)

	first, dup = laterDuplicateBatchIndex(nodes, 3)
	require.True(t, dup)
	require.Equal(t, 1, first)

	_, dup = laterDuplicateBatchIndex(nodes, 4)
	require.False(t, dup)
}

func TestCreateNode_RejectsInvalidId(t *testing.T) {
	s, _, _ := setupTestServer(t)

	post := func(path, id string) *httptest.ResponseRecorder {
		body, err := json.Marshal(apiconfig.InferenceNodeConfig{
			Id:               id,
			Host:             "localhost",
			InferencePort:    8080,
			InferenceSegment: "/api/v1",
			PoCPort:          8081,
			PoCSegment:       "/api/v1",
			MaxConcurrent:    3,
			Models:           map[string]apiconfig.ModelConfig{"test-model": {Args: []string{}}},
		})
		require.NoError(t, err)
		if path == "/admin/v1/nodes/batch" {
			body = append(append([]byte("["), body...), ']')
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		return rec
	}

	for _, id := range []string{
		"Qwen/Qwen3-4B-Instruct-2507:host--secondary",
		"node.1",
		"node:1",
		"node 1",
		"",
	} {
		rec := post("/admin/v1/nodes", id)
		require.Equal(t, http.StatusBadRequest, rec.Code, "id %q: %s", id, rec.Body.String())
		rec = post("/admin/v1/nodes/batch", id)
		require.Equal(t, http.StatusBadRequest, rec.Code, "batch id %q: %s", id, rec.Body.String())
	}
	nodes, err := s.nodeBroker.GetNodes()
	require.NoError(t, err)
	require.Empty(t, nodes)

	rec := post("/admin/v1/nodes", "Qwen3-4B_host-secondary")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	nodes, err = s.nodeBroker.GetNodes()
	require.NoError(t, err)
	require.Len(t, nodes, 1)
}
