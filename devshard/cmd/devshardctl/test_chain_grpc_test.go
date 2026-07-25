package main

import (
	"testing"

	"common/chain"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func dialTestChainGRPC(t *testing.T) *chain.Client {
	t.Helper()
	st := seed.Defaults()
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return chain.NewFromConn(conn)
}
