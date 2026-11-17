package events

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/hostevents"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/require"
)

// refusingClient fails Subscribe for selected queries. accepted is the queries
// the listener kept.
type refusingClient struct {
	mu       sync.Mutex
	calls    []string
	accepted []string
	fail     func(query string) error
}

func (f *refusingClient) Subscribe(_ context.Context, _, query string, _ ...int) (<-chan ctypes.ResultEvent, error) {
	f.mu.Lock()
	f.calls = append(f.calls, query)
	f.mu.Unlock()
	if f.fail != nil {
		if err := f.fail(query); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	f.accepted = append(f.accepted, query)
	f.mu.Unlock()
	return make(chan ctypes.ResultEvent), nil
}

func (f *refusingClient) acceptedQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.accepted))
	copy(out, f.accepted)
	return out
}

func productionListener() *Listener {
	l := NewListener("http://unused")
	l.OnDevshardEscrowCreated(func(context.Context, DevshardEscrowCreatedEvent) {})
	l.OnDevshardEscrowSettled(func(context.Context, DevshardEscrowSettledEvent) {})
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	l.ListenWarmKeys(hostevents.NewPoller(hostevents.PollerConfig{Warm: warm}))
	l.OnNewBlock(func(context.Context, NewBlockEvent) {})
	return l
}

func requiredQueries() []string {
	return []string{
		newBlockQuery,
		DevshardEscrowCreatedEvent{}.query(),
		DevshardEscrowSettledEvent{}.query(),
	}
}

func waitServe(t *testing.T, l *Listener, client *refusingClient) error {
	t.Helper()
	var ready atomic.Bool
	l.OnReady(func(v bool) {
		if v {
			ready.Store(true)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- l.serve(ctx, client) }()
	require.Eventually(t, ready.Load, time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return")
		return nil
	}
}

func TestServe_RefusedWarmKeyKeepsNewBlockAndEscrow(t *testing.T) {
	// Registration order subscribes warm_key_granted third. serve moves
	// NewBlock and both escrow queries ahead of it, so that refusal does not
	// drop them.
	l := productionListener()
	refused := errors.New("max subscriptions per client")
	client := &refusingClient{fail: func(query string) error {
		if warmKeyQuery(query) {
			return refused
		}
		return nil
	}}

	err := waitServe(t, l, client)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, requiredQueries(), client.acceptedQueries())
	require.NotContains(t, client.acceptedQueries(), hostevents.WarmKeyGrantedQuery)
	require.NotContains(t, client.acceptedQueries(), hostevents.WarmKeyRevokedQuery)
}

func TestServe_AcceptsAllFiveQueries(t *testing.T) {
	l := productionListener()
	client := &refusingClient{}

	err := waitServe(t, l, client)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, append(requiredQueries(), hostevents.WarmKeyQueries()...), client.acceptedQueries())
}

func TestServe_RefusedEscrowReturns(t *testing.T) {
	l := productionListener()
	settled := DevshardEscrowSettledEvent{}.query()
	client := &refusingClient{fail: func(query string) error {
		if query == settled {
			return errors.New("refused")
		}
		return nil
	}}
	err := l.serve(context.Background(), client)
	require.Error(t, err)
	require.ErrorContains(t, err, settled)
	require.NotContains(t, client.acceptedQueries(), hostevents.WarmKeyGrantedQuery)
}
