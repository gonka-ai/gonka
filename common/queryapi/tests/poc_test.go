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

type stubPoCServer struct {
	inferencetypes.UnimplementedQueryServer
}

func (s *stubPoCServer) PocBatchesForStage(_ context.Context, req *inferencetypes.QueryPocBatchesForStageRequest) (*inferencetypes.QueryPocBatchesForStageResponse, error) {
	if req.BlockHeight == 42 {
		return &inferencetypes.QueryPocBatchesForStageResponse{
			PocBatch: []inferencetypes.PoCBatchesWithParticipants{{}},
		}, nil
	}
	return &inferencetypes.QueryPocBatchesForStageResponse{}, nil
}

func TestGetPoCBatches_Returns200WhenFound(t *testing.T) {
	s := handlersWithInference(t, &stubPoCServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/poc-batches/42")
	require.NoError(t, s.GetPoCBatches(ctx, 42))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetPoCBatches_Returns404WhenEmpty(t *testing.T) {
	s := handlersWithInference(t, &stubPoCServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/poc-batches/99")
	err := s.GetPoCBatches(ctx, 99)
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusNotFound, httpErr.Code)
	_ = rec
}

type errPoCServer struct {
	inferencetypes.UnimplementedQueryServer
}

func (s *errPoCServer) PocBatchesForStage(_ context.Context, _ *inferencetypes.QueryPocBatchesForStageRequest) (*inferencetypes.QueryPocBatchesForStageResponse, error) {
	return nil, status.Error(codes.Unavailable, "chain unavailable")
}

func TestGetPoCBatches_ReturnsErrorOnGRPCFailure(t *testing.T) {
	s := handlersWithInference(t, &errPoCServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/poc-batches/1")
	err := s.GetPoCBatches(ctx, 1)
	require.Error(t, err)
	_ = rec
}
