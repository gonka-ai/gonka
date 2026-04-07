package queryapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/labstack/echo/v4"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"common/chain"
	"common/queryapi"
)

// fakeChain implements queryapi.ChainClient for tests.
type fakeChain struct {
	inferenceQC chain.InferenceClient
	blsQC       blstypes.QueryClient
	restrictQC  restrictionstypes.QueryClient
	cometSC     cmtservice.ServiceClient
}

func (f *fakeChain) InferenceQueryClient() chain.InferenceClient            { return f.inferenceQC }
func (f *fakeChain) BLSQueryClient() blstypes.QueryClient                   { return f.blsQC }
func (f *fakeChain) RestrictionsQueryClient() restrictionstypes.QueryClient { return f.restrictQC }
func (f *fakeChain) CometServiceClient() cmtservice.ServiceClient           { return f.cometSC }

// nopInferenceClient satisfies chain.InferenceClient with panics for all methods.
// Embed this in stubs and override only the methods you need.
type nopInferenceClient struct{}

func (nopInferenceClient) EpochInfo(_ context.Context, _ *inferencetypes.QueryEpochInfoRequest, _ ...grpc.CallOption) (*inferencetypes.QueryEpochInfoResponse, error) {
	panic("not implemented")
}
func (nopInferenceClient) ParticipantsWithBalances(_ context.Context, _ *inferencetypes.QueryParticipantsWithBalancesRequest, _ ...grpc.CallOption) (*inferencetypes.QueryParticipantsWithBalancesResponse, error) {
	panic("not implemented")
}
func (nopInferenceClient) AccountByAddress(_ context.Context, _ *inferencetypes.QueryAccountByAddressRequest, _ ...grpc.CallOption) (*inferencetypes.QueryAccountByAddressResponse, error) {
	panic("not implemented")
}
func (nopInferenceClient) Participant(_ context.Context, _ *inferencetypes.QueryGetParticipantRequest, _ ...grpc.CallOption) (*inferencetypes.QueryGetParticipantResponse, error) {
	panic("not implemented")
}
func (nopInferenceClient) DevshardEscrow(_ context.Context, _ *inferencetypes.QueryGetDevshardEscrowRequest, _ ...grpc.CallOption) (*inferencetypes.QueryGetDevshardEscrowResponse, error) {
	panic("not implemented")
}
func (nopInferenceClient) GranteesByMessageType(_ context.Context, _ *inferencetypes.QueryGranteesByMessageTypeRequest, _ ...grpc.CallOption) (*inferencetypes.QueryGranteesByMessageTypeResponse, error) {
	panic("not implemented")
}
func (nopInferenceClient) ExcludedParticipants(_ context.Context, _ *inferencetypes.QueryExcludedParticipantsRequest, _ ...grpc.CallOption) (*inferencetypes.QueryExcludedParticipantsResponse, error) {
	panic("not implemented")
}
func (nopInferenceClient) GetCurrentEpoch(_ context.Context, _ *inferencetypes.QueryGetCurrentEpochRequest, _ ...grpc.CallOption) (*inferencetypes.QueryGetCurrentEpochResponse, error) {
	panic("not implemented")
}

func echoContext(t *testing.T, method, path string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// TestNewHandlers_SatisfiesInterface verifies NewHandlers compiles with a ChainClient.
func TestNewHandlers_SatisfiesInterface(t *testing.T) {
	assert.NotNil(t, queryapi.NewHandlers(&fakeChain{}))
}

// -- GetEpoch --

type stubEpochClient struct{ nopInferenceClient }

func (s *stubEpochClient) EpochInfo(_ context.Context, _ *inferencetypes.QueryEpochInfoRequest, _ ...grpc.CallOption) (*inferencetypes.QueryEpochInfoResponse, error) {
	return &inferencetypes.QueryEpochInfoResponse{
		BlockHeight: 500,
		LatestEpoch: inferencetypes.Epoch{Index: 5, PocStartBlockHeight: 100},
	}, nil
}

func TestGetEpoch_Returns200(t *testing.T) {
	s := queryapi.NewHandlers(&fakeChain{inferenceQC: &stubEpochClient{}})
	ctx, rec := echoContext(t, http.MethodGet, "/epochs/latest")
	require.NoError(t, s.GetEpoch(ctx, "latest"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"block_height"`)
	assert.Contains(t, rec.Body.String(), `"index"`)
}

// -- GetParticipants --

type stubParticipantsClient struct{ nopInferenceClient }

func (s *stubParticipantsClient) ParticipantsWithBalances(_ context.Context, _ *inferencetypes.QueryParticipantsWithBalancesRequest, _ ...grpc.CallOption) (*inferencetypes.QueryParticipantsWithBalancesResponse, error) {
	return &inferencetypes.QueryParticipantsWithBalancesResponse{
		BlockHeight: 500,
		Participants: []inferencetypes.ParticipantWithBalance{
			{Participant: inferencetypes.Participant{Address: "gonka1abc", InferenceUrl: "http://host:8080", Weight: 10}},
		},
	}, nil
}

func TestGetParticipants_Returns200(t *testing.T) {
	s := queryapi.NewHandlers(&fakeChain{inferenceQC: &stubParticipantsClient{}})
	ctx, rec := echoContext(t, http.MethodGet, "/participants")
	require.NoError(t, s.GetParticipants(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "gonka1abc")
}

// -- GetParticipant --

type stubAccountClient struct{ nopInferenceClient }

func (s *stubAccountClient) AccountByAddress(_ context.Context, _ *inferencetypes.QueryAccountByAddressRequest, _ ...grpc.CallOption) (*inferencetypes.QueryAccountByAddressResponse, error) {
	return &inferencetypes.QueryAccountByAddressResponse{
		Pubkey:  "pubkey123",
		Balance: 9000,
		Denom:   "ngonka",
	}, nil
}

func TestGetParticipant_Returns200(t *testing.T) {
	s := queryapi.NewHandlers(&fakeChain{inferenceQC: &stubAccountClient{}})
	ctx, rec := echoContext(t, http.MethodGet, "/participants/gonka1abc")
	require.NoError(t, s.GetParticipant(ctx, "gonka1abc"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "pubkey123")
	assert.Contains(t, rec.Body.String(), "ngonka")
}
