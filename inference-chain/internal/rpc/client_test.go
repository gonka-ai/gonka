package rpc

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetStatusParsesAndFailsOnNon200(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/status", r.URL.Path)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"node_info":{"id":"node-1"},"sync_info":{"latest_block_height":"1500","earliest_block_height":"1","earliest_block_hash":"E_HASH"}}}`))
	}))
	defer ok.Close()

	status, err := getStatus(ok.URL)
	require.NoError(t, err)
	require.Equal(t, "1500", status.Result.SyncInfo.LatestBlockHeight)
	require.Equal(t, "node-1", status.Result.NodeInfo.ID)

	id, err := GetNodeId(ok.URL)
	require.NoError(t, err)
	require.Equal(t, "node-1", id)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	_, err = getStatus(bad.URL)
	require.Error(t, err)
}

func TestGetBlockHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"block":{"header":{"height":"1500"}},"block_id":{"hash":"BLOCK_HASH"}}}`))
	}))
	defer srv.Close()

	hash, err := GetBlockHash(srv.URL, 1500)
	require.NoError(t, err)
	require.Equal(t, "BLOCK_HASH", hash)

	_, err = GetBlockHash(srv.URL, 0)
	require.Error(t, err, "height 0 must be rejected before any request")
}

func TestGetTrustedBlockUsesEarliestUnderPeriod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"sync_info":{"latest_block_height":"500","earliest_block_height":"1","earliest_block_hash":"E_HASH"}}}`))
	}))
	defer srv.Close()

	height, hash, err := GetTrustedBlock(srv.URL, 1000)
	require.NoError(t, err)
	require.EqualValues(t, 1, height)
	require.Equal(t, "E_HASH", hash)
}

func TestGetTrustedBlockUsesOffsetOverPeriod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"latest_block_height":"5000","earliest_block_height":"1","earliest_block_hash":"E"}}}`))
		case "/block":
			require.Equal(t, "4000", r.URL.Query().Get("height"))
			_, _ = w.Write([]byte(`{"result":{"block":{"header":{"height":"4000"}},"block_id":{"hash":"H4000"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	height, hash, err := GetTrustedBlock(srv.URL, 1000)
	require.NoError(t, err)
	require.EqualValues(t, 4000, height)
	require.Equal(t, "H4000", hash)
}

func TestDownloadGenesis(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/genesis", r.URL.Path)
		_, _ = w.Write([]byte(`{"result":{"genesis":{"chain_id":"gonka-test"}}}`))
	}))
	defer srv.Close()

	genesis, err := DownloadGenesis(srv.URL)
	require.NoError(t, err)
	require.JSONEq(t, `{"chain_id":"gonka-test"}`, string(genesis))

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	_, err = DownloadGenesis(bad.URL)
	require.Error(t, err)
}

func TestGetBlockHashErrorsOnEmptyHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"block":{"header":{"height":""}},"block_id":{"hash":""}}}`))
	}))
	defer srv.Close()
	_, err := GetBlockHash(srv.URL, 10)
	require.Error(t, err)
}

func TestGetTrustedBlockPropagatesStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, _, err := GetTrustedBlock(srv.URL, 1000)
	require.Error(t, err)
}

// TestRPCClientBoundsStalledNode proves the shared client bounds a stalled node
// instead of hanging forever: with a short response-header timeout, a server that
// accepts the connection but never responds makes getStatus return an error
// quickly. Uses the real getStatus path with an injected short-timeout client.
func TestRPCClientBoundsStalledNode(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never send response headers
	}))
	defer stalled.Close()

	old := rpcHTTPClient
	rpcHTTPClient = &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: time.Second}).DialContext,
		ResponseHeaderTimeout: 100 * time.Millisecond,
	}}
	defer func() { rpcHTTPClient = old }()

	start := time.Now()
	_, err := getStatus(stalled.URL)
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second, "must be bounded by the timeout, not hang")
}

// TestGetStatusBoundsBodyStall covers the case the response-header timeout misses:
// a node that returns 200 headers and then stalls mid-body. The per-request
// context deadline must still bound the read instead of hanging forever.
func TestGetStatusBoundsBodyStall(t *testing.T) {
	old := statusRequestTimeout
	statusRequestTimeout = 150 * time.Millisecond
	defer func() { statusRequestTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // deliver headers, then never write the body
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	start := time.Now()
	_, err := getStatus(srv.URL)
	require.Error(t, err, "a node that sends headers then stalls the body must be bounded")
	require.Less(t, time.Since(start), 2*time.Second, "must be cut off by the request deadline")
}

func TestRPCHTTPClientIsBounded(t *testing.T) {
	tr, ok := rpcHTTPClient.Transport.(*http.Transport)
	require.True(t, ok, "rpcHTTPClient must use a configured *http.Transport")
	require.NotNil(t, tr.DialContext, "connect must be bounded")
	require.Greater(t, tr.ResponseHeaderTimeout, time.Duration(0), "response-header wait must be bounded")
}
