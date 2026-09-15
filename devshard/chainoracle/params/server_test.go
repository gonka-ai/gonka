package params_test

import (
	"context"
	"net"
	"testing"
	"time"

	"common/nodemanager/gen"
	commonruntimeconfig "common/runtimeconfig"
	"devshard/chainoracle/params"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1 << 20

func startGRPC(t *testing.T, srv *params.Server) (*grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	dial := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(dial),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	return conn, func() {
		conn.Close()
		gs.Stop()
	}
}

func TestParamsServer_GetRuntimeConfigLongPoll(t *testing.T) {
	ctx := context.Background()
	src, err := params.NewCachedSource(ctx, params.StaticFetcher{
		Snap: commonruntimeconfig.Snapshot{
			ParamsBlockHeight:       100,
			CurrentEpochID:          3,
			LogprobsMode:            "full",
			DevshardRequestsEnabled: true,
		},
	}, commonruntimeconfig.Snapshot{})
	require.NoError(t, err)

	srv, err := params.NewServer(params.Config{Source: src})
	require.NoError(t, err)

	conn, cleanup := startGRPC(t, srv)
	defer cleanup()
	client := gen.NewNodeManagerClient(conn)

	resp, err := client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{ClientParamsBlockHeight: 0})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.Equal(t, int64(100), resp.Config.ParamsBlockHeight)
	require.Equal(t, uint64(3), resp.Config.CurrentEpochId)

	resp, err = client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{ClientParamsBlockHeight: 100})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{
			ClientParamsBlockHeight: 100,
			MaxWaitSeconds:          5,
		})
		require.NoError(t, err)
		require.False(t, resp.Unchanged)
		require.Equal(t, int64(200), resp.Config.ParamsBlockHeight)
	}()

	time.Sleep(20 * time.Millisecond)
	src.SetSnapshot(commonruntimeconfig.Snapshot{
		ParamsBlockHeight:       200,
		CurrentEpochID:          4,
		DevshardRequestsEnabled: true,
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll did not wake after snapshot bump")
	}
}

func TestParamsServer_AcquireMLNode(t *testing.T) {
	ctx := context.Background()
	src, err := params.NewCachedSource(ctx, nil, commonruntimeconfig.Snapshot{})
	require.NoError(t, err)

	srv, err := params.NewServer(params.Config{
		Source:     src,
		MLEndpoint: "http://mock-openai:8088",
	})
	require.NoError(t, err)

	conn, cleanup := startGRPC(t, srv)
	defer cleanup()
	client := gen.NewNodeManagerClient(conn)

	resp, err := client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "gpt-test"})
	require.NoError(t, err)
	require.Equal(t, "http://mock-openai:8088", resp.Endpoint)
	require.NotEmpty(t, resp.LockId)
}
