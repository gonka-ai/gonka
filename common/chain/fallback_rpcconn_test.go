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
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"common/observability"
)

const (
	inferenceParamsMethod = "/inference.inference.Query/Params"
	cometNodeInfoMethod   = "/cosmos.base.tendermint.v1beta1.Service/GetNodeInfo"
)

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

func TestRPCQueryConn_CometServiceGoesThroughTheABCIRouter(t *testing.T) {
	// This pins the fallback's one known gap. CometBFT service queries are
	// tunnelled as ABCI queries like any other, so they depend on the node
	// having registered that service on its gRPC query router — which the SDK
	// only does when app.toml sets grpc.enable or api.enable. A node with both
	// off answers Unimplemented here while module queries keep working.
	var paths []string
	srv := fakeCometRPC(t, abci.ResponseQuery{Code: 0}, &paths)
	defer srv.Close()

	conn, err := newRPCQueryConn(srv.URL)
	require.NoError(t, err)

	var got cmtservice.GetNodeInfoResponse
	_ = conn.Invoke(context.Background(), cometNodeInfoMethod, &cmtservice.GetNodeInfoRequest{}, &got)

	// Not /status: the request is a store-router query addressed by method name.
	require.Equal(t, []string{cometNodeInfoMethod}, paths)
}

func TestRPCQueryConn_BroadcastTxUsesCometRPCSync(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req rpctypes.RPCRequest
		require.NoError(t, json.Unmarshal(body, &req))
		method = req.Method
		out, err := json.Marshal(rpctypes.NewRPCSuccessResponse(
			req.ID, &coretypes.ResultBroadcastTx{}))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	conn, err := newRPCQueryConn(srv.URL)
	require.NoError(t, err)
	_, err = txtypes.NewServiceClient(conn).BroadcastTx(context.Background(), &txtypes.BroadcastTxRequest{
		TxBytes: []byte("signed-tx"),
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	require.NoError(t, err)
	require.Equal(t, "broadcast_tx_sync", method)
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

func TestFallbackConn_RPCQueriesAreStillTraced(t *testing.T) {
	want := inferencetypes.QueryParamsResponse{Params: inferencetypes.DefaultParams()}
	payload, err := want.Marshal()
	require.NoError(t, err)

	srv := fakeCometRPC(t, abci.ResponseQuery{Code: 0, Value: payload, Height: 7}, nil)
	defer srv.Close()

	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	rpcConn, err := newRPCQueryConn(srv.URL)
	require.NoError(t, err)

	// The direct conn is unwrapped, so any span here came from the RPC side.
	direct := &stubConn{err: errUnavailable}
	fallback := newFallbackConn(direct, rpcConn, DefaultRPCProbeInterval, (&fakeClock{}).Now)

	_, err = inferencetypes.NewQueryClient(fallback).Params(
		context.Background(), &inferencetypes.QueryParamsRequest{})
	require.NoError(t, err)

	// Falling back must change what the spans say, not stop them: losing
	// tracing on the degraded path blinds you exactly when it matters.
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "chain.grpc.query", spans[0].Name())
	require.Equal(t, observability.TransportCometRPC, spanAttr(spans[0].Attributes(), "chain.transport"))
	require.Equal(t, "Params", spanAttr(spans[0].Attributes(), "rpc.method"))
}

func spanAttr(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}
