package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/tipcache"

	"github.com/stretchr/testify/require"
)

func TestServer_LPA5b_HTTPMatchesObservedHeader(t *testing.T) {
	cache := tipcache.New(time.Hour)
	want := blocks.HashOnlyHeader(55, time.Unix(1_704_166_245, 123456789).UTC(), "gonka-test", []byte{0xab, 0xcd})
	cache.Observe(want)

	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil, WithBlockOracle(cache))
	req := httptest.NewRequest(http.MethodGet, "/block/55", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got blocks.Header
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, want.Height, got.Height)
	require.Equal(t, want.BlockHash, got.BlockHash)
	require.Equal(t, want.ChainID, got.ChainID)
}

func TestServer_LPA5e_DisabledSkipsMountKeepsVersions(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)

	s.versionsCache.response = &versionsResponse{
		Timestamp:   "2026-01-01T00:00:00Z",
		APIVersion:  map[string]string{"version": "test"},
		NodeVersion: map[string]string{"version": "test"},
	}
	s.versionsCache.expiresAt = time.Now().Add(time.Hour)

	blockReq := httptest.NewRequest(http.MethodGet, "/block/1", nil)
	blockRec := httptest.NewRecorder()
	s.e.ServeHTTP(blockRec, blockReq)
	require.Equal(t, http.StatusNotFound, blockRec.Code)

	verReq := httptest.NewRequest(http.MethodGet, "/v1/versions", nil)
	verRec := httptest.NewRecorder()
	s.e.ServeHTTP(verRec, verReq)
	require.Equal(t, http.StatusOK, verRec.Code)
	require.Contains(t, verRec.Body.String(), `"api_version"`)
}
