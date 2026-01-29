package public

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/cosmosclient"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type fakeQueryServer struct {
	types.UnimplementedQueryServer
	firstPage  []types.ParticipantWithBalance
	secondPage []types.ParticipantWithBalance
}

func (f *fakeQueryServer) ParticipantsWithBalances(ctx context.Context, req *types.QueryParticipantsWithBalancesRequest) (*types.QueryParticipantsWithBalancesResponse, error) {
	if req.Pagination == nil || len(req.Pagination.Key) == 0 {
		return &types.QueryParticipantsWithBalancesResponse{
			Participants: f.firstPage,
			Pagination:   &query.PageResponse{NextKey: []byte("next")},
			BlockHeight:  12345,
		}, nil
	}
	return &types.QueryParticipantsWithBalancesResponse{
		Participants: f.secondPage,
		Pagination:   &query.PageResponse{NextKey: nil},
		BlockHeight:  12345,
	}, nil
}

func startBufGRPCServer(t *testing.T, srv types.QueryServer) (*grpc.ClientConn, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	types.RegisterQueryServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	dialer := func(context.Context, string) (net.Conn, error) { return listener.Dial() }
	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(dialer), grpc.WithInsecure())
	require.NoError(t, err)
	cleanup := func() { server.Stop(); _ = listener.Close(); _ = conn.Close() }
	return conn, cleanup
}

func TestGetAllParticipants_PaginationAndPinnedHeight(t *testing.T) {
	first := make([]types.ParticipantWithBalance, 100)
	second := make([]types.ParticipantWithBalance, 50)
	for i := 0; i < 100; i++ {
		first[i] = types.ParticipantWithBalance{
			Participant: types.Participant{Address: fmt.Sprintf("addr%03d", i), InferenceUrl: "http://node", CoinBalance: int64(i), Weight: int32(i)},
			Balances:    sdk.NewCoins(sdk.NewInt64Coin("ngonka", 42)),
		}
	}
	for i := 0; i < 50; i++ {
		second[i] = types.ParticipantWithBalance{
			Participant: types.Participant{Address: fmt.Sprintf("addr%03d", 100+i), InferenceUrl: "http://node", CoinBalance: int64(100 + i), Weight: int32(100 + i)},
			Balances:    sdk.NewCoins(sdk.NewInt64Coin("ngonka", 42)),
		}
	}

	fq := &fakeQueryServer{firstPage: first, secondPage: second}
	conn, cleanup := startBufGRPCServer(t, fq)
	defer cleanup()

	mc := &cosmosclient.MockCosmosMessageClient{}
	mc.On("NewInferenceQueryClient").Return(types.NewQueryClient(conn))

	e := echo.New()
	s := &Server{e: e, recorder: mc}
	req := httptest.NewRequest(http.MethodGet, "/participants", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, s.getAllParticipants(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var dto ParticipantsDto
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &dto))
	require.Equal(t, int64(12345), dto.BlockHeight)
	require.Len(t, dto.Participants, 150)
	require.Equal(t, "addr000", dto.Participants[0].Id)
	require.Equal(t, int64(42), dto.Participants[0].Balance)

	mc.AssertExpectations(t)
}
