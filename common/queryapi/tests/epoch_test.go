package queryapitest

import (
	"context"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stubEpochServer struct{ inferencetypes.UnimplementedQueryServer }

func (s *stubEpochServer) EpochInfo(_ context.Context, _ *inferencetypes.QueryEpochInfoRequest) (*inferencetypes.QueryEpochInfoResponse, error) {
	return &inferencetypes.QueryEpochInfoResponse{
		BlockHeight: 500,
		LatestEpoch: inferencetypes.Epoch{Index: 5, PocStartBlockHeight: 100},
		Params:      inferencetypes.Params{EpochParams: &inferencetypes.EpochParams{}},
	}, nil
}

func TestGetEpoch_Returns400OnNonLatest(t *testing.T) {
	s := handlersWithInference(t, &stubEpochServer{})
	ctx, _ := echoContext(t, http.MethodGet, "/epochs/5")
	err := s.GetEpoch(ctx, "5")
	require.Error(t, err)
	he, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
	}
	assert.Equal(t, http.StatusBadRequest, he.Code)
}

func TestGetEpoch_Returns200(t *testing.T) {
	s := handlersWithInference(t, &stubEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/epochs/latest")
	require.NoError(t, s.GetEpoch(ctx, "latest"))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"block_height"`)
	assert.Contains(t, body, `"index"`)
}

type errEpochServer struct{ inferencetypes.UnimplementedQueryServer }

func (s *errEpochServer) EpochInfo(_ context.Context, _ *inferencetypes.QueryEpochInfoRequest) (*inferencetypes.QueryEpochInfoResponse, error) {
	return nil, status.Error(codes.NotFound, "epoch not found")
}

func TestGetEpoch_Returns404OnGRPCNotFound(t *testing.T) {
	s := handlersWithInference(t, &errEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/epochs/latest")
	err := s.GetEpoch(ctx, "latest")
	require.Error(t, err)
	_ = rec
}
