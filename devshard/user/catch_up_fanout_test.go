package user

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"devshard/host"

	"github.com/stretchr/testify/require"
)

// Releases every waiter only once all of them have arrived.
type arrivalBarrier struct {
	remaining atomic.Int32
	released  chan struct{}
}

func (b *arrivalBarrier) wait(ctx context.Context) error {
	if b.remaining.Add(-1) == 0 {
		close(b.released)
	}
	select {
	case <-b.released:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type barrierClient struct {
	InProcessClient
	barrier *arrivalBarrier
}

func (c *barrierClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if err := c.barrier.wait(ctx); err != nil {
		return nil, err
	}
	return c.InProcessClient.Send(ctx, req, stream, receiptHandler)
}

func TestTheCatchUpReachesEveryHostAtOnce(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	_, err := session.sendPendingDiff(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, session.Diffs(), "the catch-up needs a diff to carry")

	barrier := &arrivalBarrier{released: make(chan struct{})}
	barrier.remaining.Store(int32(len(session.clients)))
	session.mu.Lock()
	for i, client := range session.clients {
		session.clients[i] = &barrierClient{InProcessClient: *client.(*InProcessClient), barrier: barrier}
		session.hostSyncNonce[i] = 0
	}
	session.mu.Unlock()

	ctx, cancelCatchUp := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCatchUp()

	require.NoError(t, session.CatchUpAllHosts(ctx),
		"a host must not wait for the one before it: the escrow stays unusable until the whole group holds the session")
}
