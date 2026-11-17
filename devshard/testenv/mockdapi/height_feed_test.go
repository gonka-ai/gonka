package mockdapi

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/headertime"
	"devshard/hostevents"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type holeSpy struct {
	mu         sync.Mutex
	headers    int
	blockReads int
}

func (h *holeSpy) Header(context.Context, int64) (*blocks.Header, error) {
	h.mu.Lock()
	h.headers++
	h.mu.Unlock()
	return nil, context.Canceled
}

func (h *holeSpy) BlockchainInfo(context.Context, int64, int64) ([]*blocks.Header, error) {
	return nil, context.Canceled
}

func (h *holeSpy) GetBlockHeader() {
	h.mu.Lock()
	h.blockReads++
	h.mu.Unlock()
}

func (h *holeSpy) counts() (headers, blockReads int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.headers, h.blockReads
}

func TestHeightFeed_PollerWritesTheChainStamp(t *testing.T) {
	feed := NewHeightFeed()
	b100, fresh := feed.Progress(100)
	require.True(t, fresh)
	time.Sleep(5 * time.Millisecond)
	b101, fresh := feed.Progress(101)
	require.True(t, fresh)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, feed.Server())
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	src := headertime.New()
	holes := &holeSpy{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client:          gen.NewNodeManagerClient(conn),
			ServerMaxWait:   time.Second,
			ErrorBackoffMin: time.Millisecond,
			ErrorBackoffMax: 5 * time.Millisecond,
		},
		Source:         src,
		BlocksVerified: true,
		Holes:          holes,
	}).Run(ctx)

	require.Eventually(t, func() bool {
		ms, err := src.HeaderTimeMs(100, b100.Hash)
		return err == nil && ms == b100.TimeMs
	}, 2*time.Second, 10*time.Millisecond)
	ms, err := src.HeaderTimeMs(101, b101.Hash)
	require.NoError(t, err)
	require.Equal(t, b101.TimeMs, ms)

	rec, ok := src.StoredAt(100)
	require.True(t, ok)
	require.Equal(t, headertime.OriginDAPI, rec.Origin)
	rec, ok = src.StoredAt(101)
	require.True(t, ok)
	require.Equal(t, headertime.OriginDAPI, rec.Origin)

	_, ok = src.StoredAt(50)
	require.False(t, ok)
	headers, blockReads := holes.counts()
	require.Zero(t, headers)
	require.Zero(t, blockReads)
}

func TestHeightFeed_FailedDAPIUsesTheChainSubscription(t *testing.T) {
	feed := NewHeightFeed()
	src := headertime.New()
	feed.ServeChain(src)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, feed.Server())
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	holes := &holeSpy{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hostevents.NewPoller(hostevents.PollerConfig{
		Config: hostevents.Config{
			Client:          gen.NewNodeManagerClient(conn),
			ServerMaxWait:   time.Second,
			ErrorBackoffMin: time.Millisecond,
			ErrorBackoffMax: 5 * time.Millisecond,
		},
		Source:         src,
		BlocksVerified: true,
		Holes:          holes,
	}).Run(ctx)

	b100, fresh := feed.Progress(100)
	require.True(t, fresh)
	require.Eventually(t, func() bool {
		rec, ok := src.StoredAt(100)
		return ok && rec.Origin == headertime.OriginDAPI && rec.TimeMs == b100.TimeMs
	}, 2*time.Second, 10*time.Millisecond)

	feed.StopClient()
	b101, fresh := feed.Progress(101)
	require.True(t, fresh)
	rec, ok := src.StoredAt(101)
	require.True(t, ok)
	require.Equal(t, headertime.OriginChain, rec.Origin)
	require.Equal(t, b101.TimeMs, rec.TimeMs)
	rec, ok = src.StoredAt(100)
	require.True(t, ok)
	require.Equal(t, b100.TimeMs, rec.TimeMs)

	feed.StartClient()
	b102, fresh := feed.Progress(102)
	require.True(t, fresh)
	require.Eventually(t, func() bool {
		rec, ok := src.StoredAt(102)
		return ok && rec.Origin == headertime.OriginDAPI && rec.TimeMs == b102.TimeMs
	}, 2*time.Second, 10*time.Millisecond)
	rec, ok = src.StoredAt(100)
	require.True(t, ok)
	require.Equal(t, b100.TimeMs, rec.TimeMs)
	rec, ok = src.StoredAt(101)
	require.True(t, ok)
	require.Equal(t, headertime.OriginChain, rec.Origin)
	require.Equal(t, b101.TimeMs, rec.TimeMs)

	headers, blockReads := holes.counts()
	require.Zero(t, headers)
	require.Zero(t, blockReads)
}
