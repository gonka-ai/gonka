package chain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const inferenceParamsMethod = "/inference.inference.Query/Params"

// fakeCometRPC serves CometBFT JSON-RPC abci_query calls, recording the ABCI
// path each request asked for.
func fakeCometRPC(t *testing.T, resp abci.ResponseQuery, paths *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req rpctypes.RPCRequest
		require.NoError(t, json.Unmarshal(body, &req))
		require.Equal(t, "abci_query", req.Method)

		var params struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.Unmarshal(req.Params, &params))
		if paths != nil {
			*paths = append(*paths, params.Path)
		}

		out, err := json.Marshal(rpctypes.NewRPCSuccessResponse(
			req.ID, &coretypes.ResultABCIQuery{Response: resp},
		))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))
}

func TestRPCQueryConn_DecodesModuleQueryResponse(t *testing.T) {
	want := inferencetypes.QueryParamsResponse{Params: inferencetypes.DefaultParams()}
	payload, err := want.Marshal()
	require.NoError(t, err)

	var paths []string
	srv := fakeCometRPC(t, abci.ResponseQuery{Code: 0, Value: payload, Height: 42}, &paths)
	defer srv.Close()

	conn, err := newRPCQueryConn(srv.URL)
	require.NoError(t, err)

	var got inferencetypes.QueryParamsResponse
	err = conn.Invoke(context.Background(), inferenceParamsMethod, &inferencetypes.QueryParamsRequest{}, &got)
	require.NoError(t, err)
	require.Equal(t, want.Params, got.Params)

	// The gRPC method name is used verbatim as the ABCI query path, which is
	// what the node's gRPC query router expects.
	require.Equal(t, []string{inferenceParamsMethod}, paths)
}

func TestRPCQueryConn_SurfacesABCIErrorAsGRPCStatus(t *testing.T) {
	srv := fakeCometRPC(t, abci.ResponseQuery{
		Code:      uint32(codes.NotFound),
		Log:       "not found",
		Codespace: "inference",
	}, nil)
	defer srv.Close()

	conn, err := newRPCQueryConn(srv.URL)
	require.NoError(t, err)

	var got inferencetypes.QueryParamsResponse
	err = conn.Invoke(context.Background(), inferenceParamsMethod, &inferencetypes.QueryParamsRequest{}, &got)
	require.Error(t, err)

	// Application-level failures must not look like a dead transport, or the
	// fallback would flap between gRPC and RPC.
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.NotEqual(t, codes.Unavailable, st.Code())
	require.False(t, isTransportDown(err))
}

func TestFallbackConn_ServesQueriesOverRPCAfterGRPCDies(t *testing.T) {
	want := inferencetypes.QueryParamsResponse{Params: inferencetypes.DefaultParams()}
	payload, err := want.Marshal()
	require.NoError(t, err)

	srv := fakeCometRPC(t, abci.ResponseQuery{Code: 0, Value: payload, Height: 7}, nil)
	defer srv.Close()

	rpcConn, err := newRPCQueryConn(srv.URL)
	require.NoError(t, err)

	direct := &stubConn{err: errUnavailable}
	clock := &fakeClock{}
	client := &Client{
		conn:      direct,
		queryConn: newFallbackConn(direct, rpcConn, DefaultRPCProbeInterval, clock.Now),
	}

	resp, err := client.InferenceQueryClient().Params(context.Background(), &inferencetypes.QueryParamsRequest{})
	require.NoError(t, err)
	require.Equal(t, want.Params, resp.Params)
}
