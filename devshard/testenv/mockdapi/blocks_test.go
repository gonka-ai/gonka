package mockdapi_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks/nmclient"
	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/headertime"
	"devshard/chainoracle/blocks/tipcache"
	"devshard/cmd/devshardd/events"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockdapi"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Height-sync floors come from mock-chain NewBlock events. A host checks a
// floor's block time through the node-manager GetBlockHeader of its dapi,
// so mock-dapi must serve the same chain: same heights, hashes and times.
func TestMockDAPI_HostResolvesMockChainFloorTimes(t *testing.T) {
	_, chainURL, stopChain, err := rpcface.NewInProcessServer(seed.Defaults(), rpcface.Config{BlockInterval: 50 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(stopChain)

	bed := startBed(t, func(cfg *mockdapi.Config) { cfg.ChainRPCAddr = chainURL })
	t.Cleanup(bed.cleanup)

	l := events.NewListener(chainURL)
	var mu sync.Mutex
	var seen []events.NewBlockEvent
	l.OnNewBlock(func(_ context.Context, e events.NewBlockEvent) {
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = l.Start(ctx) }()

	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	nm, err := nmclient.New(gen.NewNodeManagerClient(conn))
	require.NoError(t, err)
	// The host's header time source (cmd/devshardd/session/heightsync.go).
	src := headertime.New(failover.New(tipcache.New(failover.CometMaxAge), nm, nil))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 3
	}, 10*time.Second, 20*time.Millisecond)
	mu.Lock()
	got := append([]events.NewBlockEvent(nil), seen...)
	mu.Unlock()

	for _, e := range got {
		ms, err := src.AwaitHeaderTimeMs(ctx, uint64(e.BlockHeight), e.BlockHash)
		require.NoError(t, err, "host header time at %d", e.BlockHeight)
		require.Equal(t, e.Time.UnixMilli(), ms)
	}
}
