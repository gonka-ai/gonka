package queryapitest

// Tests for BLS query handlers.
// Ported from decentralized-api/internal/server/public/bls_handlers.go

import (
	"context"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- stub BLS servers ---

type stubBLSEpochServer struct{ blstypes.UnimplementedQueryServer }

func (s *stubBLSEpochServer) EpochBLSData(_ context.Context, req *blstypes.QueryEpochBLSDataRequest) (*blstypes.QueryEpochBLSDataResponse, error) {
	return &blstypes.QueryEpochBLSDataResponse{
		EpochData: blstypes.EpochBLSData{},
	}, nil
}

type errBLSEpochServer struct{ blstypes.UnimplementedQueryServer }

func (s *errBLSEpochServer) EpochBLSData(_ context.Context, _ *blstypes.QueryEpochBLSDataRequest) (*blstypes.QueryEpochBLSDataResponse, error) {
	return nil, status.Error(codes.Internal, "chain unavailable")
}

type stubBLSSignatureServer struct {
	blstypes.UnimplementedQueryServer
	req *blstypes.ThresholdSigningRequest
	err error
}

func (s *stubBLSSignatureServer) SigningStatus(_ context.Context, _ *blstypes.QuerySigningStatusRequest) (*blstypes.QuerySigningStatusResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &blstypes.QuerySigningStatusResponse{SigningRequest: *s.req}, nil
}

// --- GetBLSEpoch tests ---

func TestGetBLSEpoch_Returns200(t *testing.T) {
	h := handlersWithBLS(t, &stubBLSEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/bls/epoch/1")
	require.NoError(t, h.GetBLSEpoch(ctx, 1))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "epoch_data")
}

func TestGetBLSEpoch_Returns500OnGRPCError(t *testing.T) {
	h := handlersWithBLS(t, &errBLSEpochServer{})
	ctx, _ := echoContext(t, http.MethodGet, "/v1/bls/epoch/1")
	err := h.GetBLSEpoch(ctx, 1)
	require.Error(t, err)
	he, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
	}
	assert.Equal(t, http.StatusInternalServerError, he.Code)
}

// --- GetBLSEpochs tests ---

func TestGetBLSEpochs_Returns200(t *testing.T) {
	h := handlersWithBLS(t, &stubBLSEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/bls/epochs/2")
	require.NoError(t, h.GetBLSEpochs(ctx, 2))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "epoch_data")
}

// --- GetBLSSignature tests ---

func TestGetBLSSignature_Returns400OnInvalidHex(t *testing.T) {
	h := handlersWithBLS(t, &stubBLSSignatureServer{req: &blstypes.ThresholdSigningRequest{}})
	ctx, _ := echoContext(t, http.MethodGet, "/v1/bls/signatures/not-hex")
	err := h.GetBLSSignature(ctx, "not-hex")
	require.Error(t, err)
	he, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
	}
	assert.Equal(t, http.StatusBadRequest, he.Code)
}

func TestGetBLSSignature_Returns200WithPendingRequest(t *testing.T) {
	srv := &stubBLSSignatureServer{
		req: &blstypes.ThresholdSigningRequest{
			Status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_PENDING_SIGNING,
		},
	}
	h := handlersWithBLS(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/bls/signatures/deadbeef")
	require.NoError(t, h.GetBLSSignature(ctx, "deadbeef"))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "signing_request")
}

func TestGetBLSSignature_Returns200WithNilOnNotFound(t *testing.T) {
	srv := &stubBLSSignatureServer{
		req: &blstypes.ThresholdSigningRequest{},
		err: status.Error(codes.NotFound, "signing request not found"),
	}
	h := handlersWithBLS(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/bls/signatures/deadbeef")
	require.NoError(t, h.GetBLSSignature(ctx, "deadbeef"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "{}\n", rec.Body.String()) // omitempty: nil signing_request is omitted
}

func TestGetBLSSignature_Returns500OnGRPCError(t *testing.T) {
	srv := &stubBLSSignatureServer{
		req: &blstypes.ThresholdSigningRequest{},
		err: status.Error(codes.Internal, "backend failure"),
	}
	h := handlersWithBLS(t, srv)
	ctx, _ := echoContext(t, http.MethodGet, "/v1/bls/signatures/deadbeef")
	err := h.GetBLSSignature(ctx, "deadbeef")
	require.Error(t, err)
	he, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
	}
	assert.Equal(t, http.StatusInternalServerError, he.Code)
}
