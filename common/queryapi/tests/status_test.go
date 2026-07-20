package queryapitest

import (
	"context"
	"net/http"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetStatus_Returns200(t *testing.T) {
	s := newHandlers(&fakeChain{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/status")
	require.NoError(t, s.GetStatus(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok"`)
}

// -- GetVersions --

type stubNodeInfoServer struct{ cmtservice.UnimplementedServiceServer }

func (s *stubNodeInfoServer) GetNodeInfo(_ context.Context, _ *cmtservice.GetNodeInfoRequest) (*cmtservice.GetNodeInfoResponse, error) {
	return &cmtservice.GetNodeInfoResponse{
		ApplicationVersion: &cmtservice.VersionInfo{
			Name:      "testapp",
			Version:   "1.2.3",
			GitCommit: "abc123",
		},
	}, nil
}

func TestGetVersions_Returns200(t *testing.T) {
	s := handlersWithComet(t, &stubNodeInfoServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/versions")
	require.NoError(t, s.GetVersions(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"testapp"`)
	assert.Contains(t, body, `"1.2.3"`)
	assert.Contains(t, body, `"abc123"`)
	assert.Contains(t, body, `"api_version"`)
	assert.Contains(t, body, `"node_version"`)
	assert.Contains(t, body, `"timestamp"`)
}

type errNodeInfoServer struct{ cmtservice.UnimplementedServiceServer }

func (s *errNodeInfoServer) GetNodeInfo(_ context.Context, _ *cmtservice.GetNodeInfoRequest) (*cmtservice.GetNodeInfoResponse, error) {
	return nil, status.Error(codes.Unavailable, "node down")
}

func TestGetVersions_Returns500OnGRPCError(t *testing.T) {
	s := handlersWithComet(t, &errNodeInfoServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/versions")
	err := s.GetVersions(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error"`)
}
