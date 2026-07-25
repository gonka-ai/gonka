package public

import (
	"context"
	"net"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func startTestGRPCServer(t *testing.T, srv types.QueryServer) (*grpc.ClientConn, func()) {
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
