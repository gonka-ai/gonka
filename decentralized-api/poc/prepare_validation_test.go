package poc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type validateAllQueryServer struct {
	types.UnimplementedQueryServer
	commitQueries atomic.Int32
}

func (s *validateAllQueryServer) Params(context.Context, *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	return &types.QueryParamsResponse{Params: types.Params{PocParams: &types.PocParams{}}}, nil
}

func (s *validateAllQueryServer) AllPoCV2StoreCommitsForStage(context.Context, *types.QueryAllPoCV2StoreCommitsForStageRequest) (*types.QueryAllPoCV2StoreCommitsForStageResponse, error) {
	s.commitQueries.Add(1)
	return &types.QueryAllPoCV2StoreCommitsForStageResponse{}, nil
}

func newValidateAllQueryClient(t *testing.T, server types.QueryServer) types.QueryClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	types.RegisterQueryServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	})
	return types.NewQueryClient(conn)
}

type fixedNodeBroker struct {
	nodes  []broker.NodeResponse
	client mlnodeclient.MLNodeClient
}

func (b *fixedNodeBroker) NewNodeClient(*broker.Node) mlnodeclient.MLNodeClient { return b.client }

func (b *fixedNodeBroker) GetNodes() ([]broker.NodeResponse, error) { return b.nodes, nil }

func validationNodes(ids ...string) []broker.NodeResponse {
	nodes := make([]broker.NodeResponse, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, broker.NodeResponse{
			Node: broker.Node{Id: id, Models: map[string]broker.ModelArgs{"model-a": {}}},
			State: broker.NodeState{
				CurrentStatus: types.HardwareNodeStatus_POC,
				AdminState:    broker.AdminState{Enabled: true},
			},
		})
	}
	return nodes
}

func validationTracker() *chainphase.ChainPhaseTracker {
	tracker := &chainphase.ChainPhaseTracker{}
	tracker.Update(
		chainphase.BlockInfo{Height: 215, Hash: "fresh-hash"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{
			EpochLength:           1000,
			PocStageDuration:      100,
			PocValidationDelay:    10,
			PocValidationDuration: 100,
		},
		true,
		nil,
	)
	return tracker
}

func TestPrepareValidationNodesStopsEveryNode(t *testing.T) {
	client := mlnodeclient.NewMockClient()
	v := &OffChainValidator{
		nodeBroker:   &fixedNodeBroker{nodes: validationNodes("node-1", "node-2"), client: client},
		phaseTracker: validationTracker(),
	}

	require.True(t, v.PrepareValidationNodes())
	require.Equal(t, 2, client.StopPowV2Called)
}

func TestValidateAllDoesNotStopNodes(t *testing.T) {
	client := mlnodeclient.NewMockClient()
	queryServer := &validateAllQueryServer{}
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(newValidateAllQueryClient(t, queryServer))
	v := &OffChainValidator{
		recorder:     recorder,
		nodeBroker:   &fixedNodeBroker{nodes: validationNodes("node-1", "node-2"), client: client},
		phaseTracker: validationTracker(),
	}

	v.ValidateAll(100, "poc-start-hash")

	require.Equal(t, int32(1), queryServer.commitQueries.Load())
	require.Zero(t, client.StopPowV2Called)
}
