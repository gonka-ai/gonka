package admin

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
	"decentralized-api/cosmosclient"
	pserver "decentralized-api/internal/server/public"

	"github.com/stretchr/testify/require"
)

func TestGetBridgeQuarantine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("api: {}\n"), 0600))
	cm, err := apiconfig.LoadConfigManagerWithPaths(path, filepath.Join(dir, "config.db"), "")
	require.NoError(t, err)

	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: filepath.Join(dir, "bridge.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, apiconfig.EnsureSchema(context.Background(), db))

	amount := strings.Repeat("9", 200)
	inserted, err := apiconfig.InsertBridgeQuarantinedReceipt(context.Background(), db, apiconfig.BridgeQuarantinedReceipt{
		ChainId:         "ethereum",
		BlockNumber:     "100",
		ReceiptIndex:    "0",
		ContractAddress: "0xaa",
		OwnerPubKey:     "02aa",
		Reason:          "malformed_content",
		Error:           "amount exceeds 78 digits: invalid request",
	}, amount)
	require.NoError(t, err)
	require.True(t, inserted)

	s := NewServer(nil, nil, cm, pserver.NewBlockQueue(&cosmosclient.MockCosmosMessageClient{}, db), nil)
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/bridge/quarantine"+query, nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		return rec
	}

	rec := get("?chain=ethereum")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Chain    string                               `json:"chain"`
		Receipts []apiconfig.BridgeQuarantinedReceipt `json:"receipts"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "ethereum", resp.Chain)
	require.Len(t, resp.Receipts, 1)
	require.Equal(t, "100", resp.Receipts[0].BlockNumber)
	require.Equal(t, 200, resp.Receipts[0].AmountLen)
	require.Len(t, resp.Receipts[0].AmountPrefix, 128)

	rec = get("?chain=polygon")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"chain":"polygon","receipts":[]}`, rec.Body.String())

	require.Equal(t, http.StatusBadRequest, get("").Code)
	require.Equal(t, http.StatusBadRequest, get("?chain=ethereum&limit=0").Code)
	require.Equal(t, http.StatusBadRequest, get("?chain=ethereum&limit=x").Code)
}
