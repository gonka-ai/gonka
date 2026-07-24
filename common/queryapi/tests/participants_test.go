package queryapitest

import (
	"context"
	"net/http"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// -- GetParticipants --

type stubParticipantsServer struct {
	inferencetypes.UnimplementedQueryServer
	firstPage  []inferencetypes.ParticipantWithBalance
	secondPage []inferencetypes.ParticipantWithBalance
}

func (s *stubParticipantsServer) ParticipantsWithBalances(_ context.Context, req *inferencetypes.QueryParticipantsWithBalancesRequest) (*inferencetypes.QueryParticipantsWithBalancesResponse, error) {
	if req.Pagination == nil || len(req.Pagination.Key) == 0 {
		return &inferencetypes.QueryParticipantsWithBalancesResponse{
			BlockHeight:  12345,
			Participants: s.firstPage,
			Pagination:   &query.PageResponse{NextKey: []byte("next")},
		}, nil
	}
	return &inferencetypes.QueryParticipantsWithBalancesResponse{
		BlockHeight:  12345,
		Participants: s.secondPage,
		Pagination:   &query.PageResponse{NextKey: nil},
	}, nil
}

func TestGetParticipants_Returns200(t *testing.T) {
	srv := &stubParticipantsServer{
		firstPage: []inferencetypes.ParticipantWithBalance{
			{Participant: inferencetypes.Participant{Address: "gonka1abc", InferenceUrl: "http://host:8080", Weight: 10}},
		},
	}
	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/participants")
	require.NoError(t, s.GetParticipants(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "gonka1abc")
}

func TestGetParticipants_PaginatesAllPages(t *testing.T) {
	first := make([]inferencetypes.ParticipantWithBalance, 100)
	second := make([]inferencetypes.ParticipantWithBalance, 50)
	for i := range first {
		first[i] = inferencetypes.ParticipantWithBalance{
			Participant: inferencetypes.Participant{Address: fmt_addr(i)},
		}
	}
	for i := range second {
		second[i] = inferencetypes.ParticipantWithBalance{
			Participant: inferencetypes.Participant{Address: fmt_addr(100 + i)},
		}
	}

	s := handlersWithInference(t, &stubParticipantsServer{firstPage: first, secondPage: second})
	ctx, rec := echoContext(t, http.MethodGet, "/participants")
	require.NoError(t, s.GetParticipants(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), fmt_addr(0))
	assert.Contains(t, rec.Body.String(), fmt_addr(149))
}

func fmt_addr(i int) string {
	return "gonka1" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}

type errParticipantsServer struct{ inferencetypes.UnimplementedQueryServer }

func (s *errParticipantsServer) ParticipantsWithBalances(_ context.Context, _ *inferencetypes.QueryParticipantsWithBalancesRequest) (*inferencetypes.QueryParticipantsWithBalancesResponse, error) {
	return nil, status.Error(codes.Unavailable, "chain unavailable")
}

func TestGetParticipants_Returns503OnGRPCUnavailable(t *testing.T) {
	s := handlersWithInference(t, &errParticipantsServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/participants")
	err := s.GetParticipants(ctx)
	require.Error(t, err)
	_ = rec
}

// -- GetParticipant --

type stubAccountServer struct{ inferencetypes.UnimplementedQueryServer }

func (s *stubAccountServer) AccountByAddress(_ context.Context, _ *inferencetypes.QueryAccountByAddressRequest) (*inferencetypes.QueryAccountByAddressResponse, error) {
	return &inferencetypes.QueryAccountByAddressResponse{
		Pubkey:  "pubkey123",
		Balance: 9000,
		Denom:   "ngonka",
	}, nil
}

func TestGetParticipant_Returns200(t *testing.T) {
	s := handlersWithInference(t, &stubAccountServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/participants/gonka1abc")
	require.NoError(t, s.GetParticipant(ctx, "gonka1abc"))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "pubkey123")
	assert.Contains(t, body, "ngonka")
}

type notFoundAccountServer struct{ inferencetypes.UnimplementedQueryServer }

func (s *notFoundAccountServer) AccountByAddress(_ context.Context, _ *inferencetypes.QueryAccountByAddressRequest) (*inferencetypes.QueryAccountByAddressResponse, error) {
	return nil, status.Error(codes.NotFound, "account not found")
}

func TestGetParticipant_Returns404WhenNotFound(t *testing.T) {
	s := handlersWithInference(t, &notFoundAccountServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/participants/gonka1unknown")
	err := s.GetParticipant(ctx, "gonka1unknown")
	require.Error(t, err)
	_ = rec
}
