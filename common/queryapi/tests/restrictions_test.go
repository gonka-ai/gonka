package queryapitest

import (
	"context"
	"net/http"
	"testing"

	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// -- stub servers --

type stubRestrictionsServer struct {
	restrictionstypes.UnimplementedQueryServer
}

func (s *stubRestrictionsServer) TransferRestrictionStatus(
	_ context.Context,
	_ *restrictionstypes.QueryTransferRestrictionStatusRequest,
) (*restrictionstypes.QueryTransferRestrictionStatusResponse, error) {
	return &restrictionstypes.QueryTransferRestrictionStatusResponse{
		IsActive:            true,
		RestrictionEndBlock: 1000,
		CurrentBlockHeight:  500,
		RemainingBlocks:     500,
	}, nil
}

func (s *stubRestrictionsServer) TransferExemptions(
	_ context.Context,
	_ *restrictionstypes.QueryTransferExemptionsRequest,
) (*restrictionstypes.QueryTransferExemptionsResponse, error) {
	return &restrictionstypes.QueryTransferExemptionsResponse{
		Exemptions: []restrictionstypes.EmergencyTransferExemption{},
	}, nil
}

func (s *stubRestrictionsServer) ExemptionUsage(
	_ context.Context,
	req *restrictionstypes.QueryExemptionUsageRequest,
) (*restrictionstypes.QueryExemptionUsageResponse, error) {
	return &restrictionstypes.QueryExemptionUsageResponse{
		UsageEntries: []restrictionstypes.ExemptionUsage{},
	}, nil
}

type errRestrictionsServer struct {
	restrictionstypes.UnimplementedQueryServer
}

func (s *errRestrictionsServer) TransferRestrictionStatus(
	_ context.Context,
	_ *restrictionstypes.QueryTransferRestrictionStatusRequest,
) (*restrictionstypes.QueryTransferRestrictionStatusResponse, error) {
	return nil, status.Error(codes.Unavailable, "chain unavailable")
}

func (s *errRestrictionsServer) TransferExemptions(
	_ context.Context,
	_ *restrictionstypes.QueryTransferExemptionsRequest,
) (*restrictionstypes.QueryTransferExemptionsResponse, error) {
	return nil, status.Error(codes.Unavailable, "chain unavailable")
}

func (s *errRestrictionsServer) ExemptionUsage(
	_ context.Context,
	_ *restrictionstypes.QueryExemptionUsageRequest,
) (*restrictionstypes.QueryExemptionUsageResponse, error) {
	return nil, status.Error(codes.NotFound, "exemption not found")
}

// -- GetRestrictionsStatus tests --

func TestGetRestrictionsStatus_Returns200(t *testing.T) {
	s := handlersWithRestrictions(t, &stubRestrictionsServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/restrictions/status")
	require.NoError(t, s.GetRestrictionsStatus(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"is_active"`)
}

func TestGetRestrictionsStatus_PropagatesGRPCError(t *testing.T) {
	s := handlersWithRestrictions(t, &errRestrictionsServer{})
	ctx, _ := echoContext(t, http.MethodGet, "/v1/restrictions/status")
	err := s.GetRestrictionsStatus(ctx)
	require.Error(t, err)
}

// -- GetRestrictionsExemptions tests --

func TestGetRestrictionsExemptions_Returns200(t *testing.T) {
	s := handlersWithRestrictions(t, &stubRestrictionsServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/restrictions/exemptions")
	require.NoError(t, s.GetRestrictionsExemptions(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetRestrictionsExemptions_PropagatesGRPCError(t *testing.T) {
	s := handlersWithRestrictions(t, &errRestrictionsServer{})
	ctx, _ := echoContext(t, http.MethodGet, "/v1/restrictions/exemptions")
	err := s.GetRestrictionsExemptions(ctx)
	require.Error(t, err)
}

// -- GetRestrictionsExemptionUsage tests --

func TestGetRestrictionsExemptionUsage_Returns200(t *testing.T) {
	s := handlersWithRestrictions(t, &stubRestrictionsServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/restrictions/exemptions/ex1/usage/addr1")
	require.NoError(t, s.GetRestrictionsExemptionUsage(ctx, "ex1", "addr1"))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetRestrictionsExemptionUsage_Returns404OnNotFound(t *testing.T) {
	s := handlersWithRestrictions(t, &errRestrictionsServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/restrictions/exemptions/ex1/usage/addr1")
	err := s.GetRestrictionsExemptionUsage(ctx, "ex1", "addr1")
	require.Error(t, err)
	_ = rec
}
