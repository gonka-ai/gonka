package events

import (
	"context"
	"sync"
	"testing"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

// TestSubscribe_CoalescesSameQuery guards the CometBFT WS client quirk that
// keeps only one channel per query. Two OnNewBlock registrations must share a
// single subscription and both handlers must fire (phase update + prune).
func TestSubscribe_CoalescesSameQuery(t *testing.T) {
	l := NewListener("http://unused")

	var (
		mu    sync.Mutex
		order []string
	)
	l.OnNewBlock(func(_ context.Context, e NewBlockEvent) {
		mu.Lock()
		order = append(order, "phase")
		mu.Unlock()
		require.Equal(t, int64(42), e.BlockHeight)
	})
	l.OnNewBlock(func(_ context.Context, e NewBlockEvent) {
		mu.Lock()
		order = append(order, "prune")
		mu.Unlock()
		require.Equal(t, int64(42), e.BlockHeight)
	})

	require.Len(t, l.subs, 1, "duplicate NewBlock queries must coalesce")
	require.Equal(t, "tm.event='NewBlock'", l.subs[0].query)
	require.Len(t, l.subs[0].handlers, 2)

	result := ctypes.ResultEvent{
		Query: "tm.event='NewBlock'",
		Data: cmttypes.EventDataNewBlock{
			Block: &cmttypes.Block{
				Header: cmttypes.Header{Height: 42},
			},
		},
	}
	for _, h := range l.subs[0].handlers {
		h(context.Background(), result)
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"phase", "prune"}, order, "handlers must run in registration order")
}

func TestSubscribe_DistinctQueriesStaySeparate(t *testing.T) {
	l := NewListener("http://unused")
	l.OnNewBlock(func(context.Context, NewBlockEvent) {})
	l.OnDevshardEscrowCreated(func(context.Context, DevshardEscrowCreatedEvent) {})
	l.OnDevshardEscrowSettled(func(context.Context, DevshardEscrowSettledEvent) {})

	require.Len(t, l.subs, 3)
	queries := map[string]int{}
	for _, sub := range l.subs {
		queries[sub.query] = len(sub.handlers)
	}
	require.Equal(t, 1, queries["tm.event='NewBlock'"])
	require.Equal(t, 1, queries[DevshardEscrowCreatedEvent{}.query()])
	require.Equal(t, 1, queries[DevshardEscrowSettledEvent{}.query()])
}
