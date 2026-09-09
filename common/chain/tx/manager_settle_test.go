package tx

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type settleTxStub struct {
	txtypes.UnimplementedServiceServer
	getHits atomic.Int32
	mode    string // "ok" | "deliver_fail" | "not_found_then_ok"
}

func (s *settleTxStub) BroadcastTx(_ context.Context, _ *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
	return &txtypes.BroadcastTxResponse{
		TxResponse: &sdk.TxResponse{Code: 0, TxHash: "SETTLEHASH"},
	}, nil
}

func (s *settleTxStub) GetTx(_ context.Context, req *txtypes.GetTxRequest) (*txtypes.GetTxResponse, error) {
	n := s.getHits.Add(1)
	switch s.mode {
	case "deliver_fail":
		return &txtypes.GetTxResponse{
			TxResponse: &sdk.TxResponse{
				Code:      7,
				Codespace: "inference",
				RawLog:    "deliver failed",
				TxHash:    req.GetHash(),
			},
		}, nil
	case "not_found_then_ok":
		if n == 1 {
			return nil, status.Error(codes.NotFound, "tx not found")
		}
		return &txtypes.GetTxResponse{
			TxResponse: &sdk.TxResponse{Code: 0, TxHash: req.GetHash()},
		}, nil
	default:
		return &txtypes.GetTxResponse{
			TxResponse: &sdk.TxResponse{Code: 0, TxHash: req.GetHash()},
		}, nil
	}
}

func startSettleTxStub(t *testing.T, stub *settleTxStub) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	txtypes.RegisterServiceServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestWaitForTxSuccess_DeliverTxFailure(t *testing.T) {
	stub := &settleTxStub{mode: "deliver_fail"}
	conn := startSettleTxStub(t, stub)
	m, err := New(conn, Config{
		ChainID:      "gonka-test",
		PollInterval: time.Millisecond,
		PollTimeout:  200 * time.Millisecond,
	})
	require.NoError(t, err)

	err = m.waitForTxSuccess(t.Context(), "SETTLEHASH")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed code=7")
}

func TestWaitForTxSuccess_PollsUntilIndexed(t *testing.T) {
	stub := &settleTxStub{mode: "not_found_then_ok"}
	conn := startSettleTxStub(t, stub)
	m, err := New(conn, Config{
		ChainID:      "gonka-test",
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
	})
	require.NoError(t, err)

	require.NoError(t, m.waitForTxSuccess(t.Context(), "SETTLEHASH"))
	require.GreaterOrEqual(t, stub.getHits.Load(), int32(2))
}
