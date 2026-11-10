package admin

import (
	"bytes"
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

func TestDevshardVersionOverrideEndpoints(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("api: {}\n"), 0600))
	cm, err := apiconfig.LoadConfigManagerWithPaths(path, filepath.Join(dir, "config.db"), "")
	require.NoError(t, err)
	s := NewServer(nil, nil, cm, nil, nil)
	request := func(method string, body any) *httptest.ResponseRecorder {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, "/admin/v1/devshard/versions", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		return rec
	}
	o := apiconfig.DevshardVersionOverride{
		From: apiconfig.DevshardBinary{Binary: "https://example.com/a", SHA256: strings.Repeat("a", 64)},
		To:   apiconfig.DevshardBinary{Binary: "https://example.com/b", SHA256: strings.Repeat("b", 64)},
	}
	require.JSONEq(t, "[]", request(http.MethodGet, nil).Body.String())
	require.Equal(t, http.StatusOK, request(http.MethodPost, o).Code)
	var listed []apiconfig.DevshardVersionOverride
	get := request(http.MethodGet, nil)
	require.Equal(t, http.StatusOK, get.Code)
	require.NoError(t, json.Unmarshal(get.Body.Bytes(), &listed))
	require.Equal(t, []apiconfig.DevshardVersionOverride{o}, listed)
	for _, invalid := range []apiconfig.DevshardBinary{
		{Binary: "file:///tmp/binary", SHA256: o.To.SHA256},
		{Binary: "/relative", SHA256: o.To.SHA256},
		{Binary: o.To.Binary, SHA256: "bad"},
		{Binary: o.To.Binary, SHA256: strings.Repeat("z", 64)},
		{},
	} {
		require.Equal(t, http.StatusBadRequest, request(http.MethodPost, apiconfig.DevshardVersionOverride{From: o.From, To: invalid}).Code)
		require.Equal(t, http.StatusBadRequest, request(http.MethodPost, apiconfig.DevshardVersionOverride{From: invalid, To: o.To}).Code)
		require.Equal(t, http.StatusBadRequest, request(http.MethodDelete, map[string]any{"from": invalid}).Code)
	}
	require.Equal(t, []apiconfig.DevshardVersionOverride{o}, cm.GetDevshardVersionOverrides())
	require.Equal(t, http.StatusNoContent, request(http.MethodDelete, map[string]any{"from": o.From}).Code)
	require.JSONEq(t, "[]", request(http.MethodGet, nil).Body.String())
}
