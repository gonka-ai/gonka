package tx

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type blockingBroadcastStub struct {
	txtypes.UnimplementedServiceServer
	release chan struct{}
	calls   atomic.Int32
	current atomic.Int32
	peak    atomic.Int32
}

func (s *blockingBroadcastStub) BroadcastTx(context.Context, *txtypes.BroadcastTxRequest) (*txtypes.BroadcastTxResponse, error) {
	s.calls.Add(1)
	now := s.current.Add(1)
	defer s.current.Add(-1)
	for {
		seen := s.peak.Load()
		if now <= seen || s.peak.CompareAndSwap(seen, now) {
			break
		}
	}
	<-s.release
	return &txtypes.BroadcastTxResponse{TxResponse: &sdk.TxResponse{TxHash: "HASH"}}, nil
}

func startBroadcastStub(t *testing.T, stub *blockingBroadcastStub, slots chan struct{}) *Manager {
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
	m, err := New(conn, Config{BroadcastSlots: slots})
	require.NoError(t, err)
	return m
}

func TestBroadcastSlotsCapConcurrentBroadcasts(t *testing.T) {
	stub := &blockingBroadcastStub{release: make(chan struct{})}
	m := startBroadcastStub(t, stub, make(chan struct{}, 2))

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.broadcastTx(context.Background(), []byte("tx"))
		}()
	}
	require.Eventually(t, func() bool { return stub.current.Load() == 2 }, 2*time.Second, time.Millisecond)
	require.Never(t, func() bool { return stub.calls.Load() > 2 }, 50*time.Millisecond, time.Millisecond)

	close(stub.release)
	wg.Wait()
	require.EqualValues(t, 6, stub.calls.Load())
	require.EqualValues(t, 2, stub.peak.Load())
}

func TestBroadcastSlotIsReleasedWhenBroadcastReturns(t *testing.T) {
	stub := &blockingBroadcastStub{release: make(chan struct{})}
	close(stub.release)
	slots := make(chan struct{}, 1)
	m := startBroadcastStub(t, stub, slots)

	_, err := m.broadcastTx(context.Background(), []byte("tx"))
	require.NoError(t, err)
	require.Empty(t, slots)
}

func TestBroadcastWaiterStopsWhenContextEnds(t *testing.T) {
	stub := &blockingBroadcastStub{release: make(chan struct{})}
	close(stub.release)
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	m := startBroadcastStub(t, stub, slots)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := m.broadcastTx(ctx, []byte("tx"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, stub.calls.Load())
}

func TestBroadcastCanceledContextDoesNotBroadcast(t *testing.T) {
	stub := &blockingBroadcastStub{release: make(chan struct{})}
	close(stub.release)
	m := startBroadcastStub(t, stub, make(chan struct{}, 1))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.broadcastTx(ctx, []byte("tx"))
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, stub.calls.Load())
}

type contextIgnoringConn struct {
	grpc.ClientConnInterface
	calls atomic.Int32
}

func (c *contextIgnoringConn) Invoke(_ context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	c.calls.Add(1)
	reply.(*txtypes.BroadcastTxResponse).TxResponse = &sdk.TxResponse{TxHash: "HASH"}
	return nil
}

type canceledAfterFirstCheckContext struct {
	context.Context
	checks atomic.Int32
	done   chan struct{}
}

func (c *canceledAfterFirstCheckContext) Done() <-chan struct{} { return c.done }

func (c *canceledAfterFirstCheckContext) Err() error {
	if c.checks.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

func TestBroadcastCancellationRacingAdmissionDoesNotBroadcast(t *testing.T) {
	conn := &contextIgnoringConn{}
	slots := make(chan struct{}, 1)
	m, err := New(conn, Config{BroadcastSlots: slots})
	require.NoError(t, err)

	closed := make(chan struct{})
	close(closed)
	for trial := 0; trial < 200; trial++ {
		ctx := &canceledAfterFirstCheckContext{Context: context.Background(), done: closed}
		_, err := m.broadcastTx(ctx, []byte("tx"))
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, slots)
	}
	require.Zero(t, conn.calls.Load())
}

func TestBroadcastWithoutSlotsIsUnlimited(t *testing.T) {
	stub := &blockingBroadcastStub{release: make(chan struct{})}
	close(stub.release)
	m := startBroadcastStub(t, stub, nil)

	hash, err := m.broadcastTx(context.Background(), []byte("tx"))
	require.NoError(t, err)
	require.Equal(t, "HASH", hash)
}
