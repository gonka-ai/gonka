package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/observer"
	blockserver "devshard/chainoracle/blocks/server"
	"devshard/chainoracle/server"
	"devshard/signing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func newMockOracle(t *testing.T) *observer.Mock {
	t.Helper()
	signer, err := signing.GenerateKey()
	require.NoError(t, err)
	addr, err := blocks.AddressBytes(signer.PublicKeyBytes())
	require.NoError(t, err)
	mock, err := observer.NewMock(observer.MockConfig{
		ChainID: "gonka-test",
		Validators: []observer.MockValidator{
			{Signer: signer, Address: addr, Power: 1},
		},
		BlockInterval: time.Second,
		Seed:          1,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	require.NoError(t, err)
	return mock
}

func TestMount_VersionsAndBlocks(t *testing.T) {
	mock := newMockOracle(t)
	_, err := mock.AdvanceOne()
	require.NoError(t, err)

	e := echo.New()
	server.Mount(e.Group(""), server.Config{
		Blocks: mock,
		Versions: []server.Version{
			{Name: "v0.2.13", Binary: "devshard", SHA256: "abc"},
		},
	})
	ts := httptest.NewServer(e)
	defer ts.Close()

	// /versions
	resp, err := http.Get(ts.URL + "/versions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var cfg server.VersionConfig
	require.NoError(t, json.Unmarshal(body, &cfg))
	require.Len(t, cfg.Versions, 1)
	require.Equal(t, "v0.2.13", cfg.Versions[0].Name)

	// /block/latest (delegated to blocks/server)
	resp2, err := http.Get(ts.URL + "/block/latest")
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	// healthz from blocks/server
	resp3, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp3.Body.Close()
	require.Equal(t, http.StatusOK, resp3.StatusCode)
}

// Compile-time check that blockserver.Mount remains reachable.
var _ = blockserver.Mount
