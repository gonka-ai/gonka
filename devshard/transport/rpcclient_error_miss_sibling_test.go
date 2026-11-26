package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	devtest "devshard/internal/testutil"
	"devshard/transport"
	"devshard/transport/rpcserver"
	"devshard/types"
)

type errorMissSiblingCore struct {
	largeRPCCore
	sibling *atomic.Uint64
}

func (c errorMissSiblingCore) ServeVerifyErrorMiss(_ context.Context, req transport.VerifyErrorMissRequest) (*transport.VerifyErrorMissResponse, error) {
	c.sibling.Store(req.SiblingInferenceID)
	return &transport.VerifyErrorMissResponse{}, nil
}

type errorMissSiblingLookup struct {
	core errorMissSiblingCore
}

func (l errorMissSiblingLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return l.core, nil
}

func (l errorMissSiblingLookup) SessionForParticipant(id, _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}

func (l errorMissSiblingLookup) SessionForOwner(id, _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}

func (l errorMissSiblingLookup) SessionForStartProof(id, _ string, _ func() ([]types.Diff, error), _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}

func TestRPCClient_VerifyErrorMissCarriesTheServedSibling(t *testing.T) {
	artifacts := host.TimeoutArtifacts{FinishTx: []byte{1}, ResponsePayload: []byte("err"), SiblingInferenceID: 42}

	t.Run("connect", func(t *testing.T) {
		var sibling atomic.Uint64
		rpc := newLargeRPCClient(t, errorMissSiblingLookup{core: errorMissSiblingCore{sibling: &sibling}},
			transport.EndpointVerifyErrorMiss, transport.DefaultClientConfig())
		require.True(t, rpc.Uses(transport.EndpointVerifyErrorMiss))

		_, _, _, _, _, err := rpc.VerifyErrorMiss(context.Background(), 7, nil, artifacts)

		require.NoError(t, err)
		require.Equal(t, uint64(42), sibling.Load())
	})

	t.Run("http fallback", func(t *testing.T) {
		var sibling atomic.Uint64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/sessions/escrow-1/verify-error-miss") {
				http.NotFound(w, r)
				return
			}
			var req transport.VerifyErrorMissRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			sibling.Store(req.SiblingInferenceID)
			require.NoError(t, json.NewEncoder(w).Encode(transport.VerifyErrorMissResponse{}))
		}))
		t.Cleanup(srv.Close)
		cfg := transport.DefaultClientConfig()
		cfg.AllowRetiredHTTPSession = true
		rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", devtest.MustGenerateKey(t), cfg), nil, transport.ParseRPCEndpoints(""))
		require.False(t, rpc.Uses(transport.EndpointVerifyErrorMiss))

		_, _, _, _, _, err := rpc.VerifyErrorMiss(context.Background(), 7, nil, artifacts)

		require.NoError(t, err)
		require.Equal(t, uint64(42), sibling.Load())
	})
}
