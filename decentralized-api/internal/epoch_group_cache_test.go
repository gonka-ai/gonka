package internal

import (
	"context"
	"net"
	"sync"
	"testing"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type mutableEpochGroupQueryServer struct {
	types.UnimplementedQueryServer
	mu   sync.Mutex
	data types.EpochGroupData
}

func (s *mutableEpochGroupQueryServer) EpochGroupData(
	_ context.Context,
	_ *types.QueryGetEpochGroupDataRequest,
) (*types.QueryGetEpochGroupDataResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: s.data}, nil
}

func (s *mutableEpochGroupQueryServer) set(data types.EpochGroupData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
}

func startEpochGroupCacheTestServer(t *testing.T, srv types.QueryServer) (*grpc.ClientConn, func()) {
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

func TestIsActiveParticipant_RefetchesEmptyCachedEpoch(t *testing.T) {
	queryServer := &mutableEpochGroupQueryServer{
		data: types.EpochGroupData{
			// Epoch start: models may exist before ValidationWeights are assigned.
			SubGroupModels: []string{"Qwen/Qwen2.5-7B-Instruct"},
		},
	}
	conn, cleanup := startEpochGroupCacheTestServer(t, queryServer)
	t.Cleanup(cleanup)

	mockCosmos := &cosmosclient.MockCosmosMessageClient{}
	mockCosmos.On("NewInferenceQueryClient").Return(types.NewQueryClient(conn))

	cache := NewEpochGroupDataCache(mockCosmos)

	// First fetch (as refreshModelValidationThresholds does at epoch start) caches 0 participants.
	_, err := cache.GetEpochGroupData(context.Background(), 2)
	require.NoError(t, err)

	active, err := cache.IsActiveParticipant(context.Background(), 2, "gonka1participant")
	require.NoError(t, err)
	require.False(t, active)

	// Later, members are assigned on-chain.
	queryServer.set(types.EpochGroupData{
		SubGroupModels: []string{"Qwen/Qwen2.5-7B-Instruct"},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "gonka1participant"},
		},
	})

	active, err = cache.IsActiveParticipant(context.Background(), 2, "gonka1participant")
	require.NoError(t, err)
	require.True(t, active, "empty pre-member cache must be refreshed once ValidationWeights exist")
}
