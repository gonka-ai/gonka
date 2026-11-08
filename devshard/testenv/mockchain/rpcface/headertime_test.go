package rpcface_test

import (
	"context"
	"sync"
	"testing"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks/observer"

	"devshard/chainoracle/blocks/headertime"
	"devshard/cmd/devshardd/events"
)

// Hosts take the height-sync floor hash and time from Comet NewBlock events.
// The gateway resolves the same height through a commit-verifying light
// client (cmd/devshardctl gatewayHeaderTimeSource). On the mock chain both
// views must agree, or every Start, ConfirmStart and Finish waits for a
// header time that never resolves.
func TestMockChainRPC_GatewayVerifiesHostFloorTimes(t *testing.T) {
	url, _ := startTestRPC(t, 50*time.Millisecond)

	l := events.NewListener(url)
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

	waitFor := func(n int) []events.NewBlockEvent {
		t.Helper()
		require.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(seen) >= n
		}, 10*time.Second, 20*time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return append([]events.NewBlockEvent(nil), seen...)
	}

	client, err := rpchttp.New(url, "/websocket")
	require.NoError(t, err)
	oracle, err := observer.NewTendermint(observer.TendermintConfig{RPCURL: url, VerifyCommits: true})
	require.NoError(t, err)
	src := headertime.New(oracle)

	check := func(e events.NewBlockEvent) {
		t.Helper()
		h := e.BlockHeight
		ms, err := src.AwaitHeaderTimeMs(ctx, uint64(h), e.BlockHash)
		require.NoError(t, err, "gateway header time at %d", h)
		require.False(t, e.Time.IsZero(), "NewBlock %d carries no block time", h)
		require.Equal(t, e.Time.UnixMilli(), ms)

		block, err := client.Block(ctx, &h)
		require.NoError(t, err)
		require.Equal(t, e.BlockHash, block.BlockID.Hash.Bytes(), "block RPC hash differs from NewBlock at %d", h)
	}

	first := waitFor(3)
	// The first lookup trusts the current tip, so these heights resolve
	// through the backward hash links.
	for _, e := range first {
		check(e)
	}
	// Later heights resolve forward through light-client verification.
	later := waitFor(len(first) + 3)
	for _, e := range later[len(first):] {
		check(e)
	}
}
