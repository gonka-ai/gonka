package admin

import (
	"bytes"
	"decentralized-api/apiconfig"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestNodeAdminRoutes_IdWithSlash(t *testing.T) {
	s, _, _ := setupTestServer(t)

	nodeID := "Qwen/Qwen3-4B-Instruct-2507:host--secondary"
	body, err := json.Marshal(apiconfig.InferenceNodeConfig{
		Id:               nodeID,
		Host:             "localhost",
		InferencePort:    8080,
		InferenceSegment: "/api/v1",
		PoCPort:          8081,
		PoCSegment:       "/api/v1",
		MaxConcurrent:    3,
		Models:           map[string]apiconfig.ModelConfig{"test-model": {Args: []string{}}},
	})
	require.NoError(t, err)

	do := func(method, path string, reqBody []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		return rec
	}
	enabledOf := func() bool {
		nodes, err := s.nodeBroker.GetNodes()
		require.NoError(t, err)
		require.Len(t, nodes, 1)
		return nodes[0].State.AdminState.Enabled
	}

	base := "/admin/v1/nodes/" + url.PathEscape(nodeID)

	require.Equal(t, http.StatusOK, do(http.MethodPost, "/admin/v1/nodes", body).Code)
	require.True(t, enabledOf())

	rec := do(http.MethodPost, base+"/disable", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, enabledOf())

	rec = do(http.MethodPost, base+"/enable", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, enabledOf())

	rec = do(http.MethodDelete, base, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, "true", rec.Body.String())
	nodes, err := s.nodeBroker.GetNodes()
	require.NoError(t, err)
	require.Empty(t, nodes)
}

func TestNodeIdParam(t *testing.T) {
	e := echo.New()
	for _, tc := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "node-1", want: "node-1"},
		{raw: "Qwen%2FQwen3-4B%3Ahost", want: "Qwen/Qwen3-4B:host"},
		{raw: "", wantErr: true},
		{raw: "bad%zz", wantErr: true},
	} {
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
		c.SetParamNames("id")
		c.SetParamValues(tc.raw)
		got, err := nodeIdParam(c)
		if tc.wantErr {
			var he *echo.HTTPError
			require.ErrorAs(t, err, &he, tc.raw)
			require.Equal(t, http.StatusBadRequest, he.Code, tc.raw)
			continue
		}
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, got, tc.raw)
	}
}
