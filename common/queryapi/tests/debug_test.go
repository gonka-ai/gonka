package queryapitest

import (
	"context"
	"net/http"
	"testing"

	cometproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// -- DebugPubKeyToAddr --
// Test vector from decentralized-api/apiconfig/accounts_test.go

func TestDebugPubKeyToAddr_KnownVector(t *testing.T) {
	s := newHandlers(&fakeChain{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/debug/pubkey-to-addr/Au5ZQav3E36PZpGta2xUa8r9xEEo9Biph3fG5i3qaeSG")
	require.NoError(t, s.DebugPubKeyToAddr(ctx, "Au5ZQav3E36PZpGta2xUa8r9xEEo9Biph3fG5i3qaeSG"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gonka1jwrv4q8hpxc354pr87pt0pkulaep67e9s4z0ym", rec.Body.String())
}

func TestDebugPubKeyToAddr_InvalidPubKey(t *testing.T) {
	s := newHandlers(&fakeChain{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/debug/pubkey-to-addr/notvalid!!!")
	err := s.DebugPubKeyToAddr(ctx, "notvalid!!!")
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadRequest, httpErr.Code)
	_ = rec
}

// -- DebugVerifyBlockSignatures --

type errBlockServer struct{ cmtservice.UnimplementedServiceServer }

func (s *errBlockServer) GetBlockByHeight(_ context.Context, _ *cmtservice.GetBlockByHeightRequest) (*cmtservice.GetBlockByHeightResponse, error) {
	return nil, status.Error(codes.NotFound, "block not found")
}

func TestDebugVerifyBlockSignatures_Returns404WhenBlockNotFound(t *testing.T) {
	s := handlersWithComet(t, &errBlockServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/debug/verify/100")
	err := s.DebugVerifyBlockSignatures(ctx, 100)
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusNotFound, httpErr.Code)
	_ = rec
}

type stubVerifyBlockServer struct {
	cmtservice.UnimplementedServiceServer
	blockHeight int64
}

func (s *stubVerifyBlockServer) GetBlockByHeight(_ context.Context, _ *cmtservice.GetBlockByHeightRequest) (*cmtservice.GetBlockByHeightResponse, error) {
	return &cmtservice.GetBlockByHeightResponse{
		SdkBlock: &cmtservice.Block{
			Header:     cmtservice.Header{ChainID: "gonka-1", Height: s.blockHeight},
			LastCommit: &cometproto.Commit{},
		},
	}, nil
}

func (s *stubVerifyBlockServer) GetValidatorSetByHeight(_ context.Context, _ *cmtservice.GetValidatorSetByHeightRequest) (*cmtservice.GetValidatorSetByHeightResponse, error) {
	return &cmtservice.GetValidatorSetByHeightResponse{}, nil
}

func TestDebugVerifyBlockSignatures_Returns400OnInvalidSignatures(t *testing.T) {
	s := handlersWithComet(t, &stubVerifyBlockServer{blockHeight: 100})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/debug/verify/100")
	err := s.DebugVerifyBlockSignatures(ctx, 100)
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadRequest, httpErr.Code)
	_ = rec
}
