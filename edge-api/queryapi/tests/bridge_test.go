package queryapitest

import (
	"context"
	"net/http"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"edge-api/queryapi/gen"
)

type stubBridgeServer struct {
	inferencetypes.UnimplementedQueryServer
}

func (s *stubBridgeServer) BridgeAddressesByChain(_ context.Context, req *inferencetypes.QueryBridgeAddressesByChainRequest) (*inferencetypes.QueryBridgeAddressesByChainResponse, error) {
	if req.ChainId != "ethereum" {
		return &inferencetypes.QueryBridgeAddressesByChainResponse{}, nil
	}
	return &inferencetypes.QueryBridgeAddressesByChainResponse{
		Addresses: []inferencetypes.BridgeContractAddress{
			{Address: "0xdeadbeef"},
			{Address: "0xcafebabe"},
		},
	}, nil
}

func TestGetBridgeAddresses_Returns200(t *testing.T) {
	s := handlersWithInference(t, &stubBridgeServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/bridge/addresses?chain=ethereum")
	require.NoError(t, s.GetBridgeAddresses(ctx, gen.GetBridgeAddressesParams{Chain: "ethereum"}))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "0xdeadbeef")
	assert.Contains(t, body, "0xcafebabe")
	assert.Contains(t, body, `"chain_name":"ethereum"`)
}

func TestGetBridgeAddresses_ReturnsEmptyForUnknownChain(t *testing.T) {
	s := handlersWithInference(t, &stubBridgeServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/bridge/addresses?chain=unknown")
	require.NoError(t, s.GetBridgeAddresses(ctx, gen.GetBridgeAddressesParams{Chain: "unknown"}))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"addresses":null`)
}

type errBridgeServer struct {
	inferencetypes.UnimplementedQueryServer
}

func (s *errBridgeServer) BridgeAddressesByChain(_ context.Context, _ *inferencetypes.QueryBridgeAddressesByChainRequest) (*inferencetypes.QueryBridgeAddressesByChainResponse, error) {
	return nil, status.Error(codes.NotFound, "chain not found")
}

func TestGetBridgeAddresses_Returns500OnGRPCError(t *testing.T) {
	s := handlersWithInference(t, &errBridgeServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/bridge/addresses?chain=unknown")
	err := s.GetBridgeAddresses(ctx, gen.GetBridgeAddressesParams{Chain: "unknown"})
	require.Error(t, err)
	_ = rec
}
