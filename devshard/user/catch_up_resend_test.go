package user

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"devshard/host"
	"devshard/storage"
	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// faultyChunkClient fails the first len(errs) sends with the scripted errors
// and records the diff range of every send. A nil error, or a send past the
// script, goes to the host. block makes a send wait for its context or gate.
type faultyChunkClient struct {
	*InProcessClient
	mu    sync.Mutex
	errs  []error
	sends [][2]uint64
	gate  chan struct{}
}

func (c *faultyChunkClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	c.mu.Lock()
	if len(req.Diffs) > 0 {
		c.sends = append(c.sends, [2]uint64{req.Diffs[0].Nonce, req.Diffs[len(req.Diffs)-1].Nonce})
	}
	var err error
	if len(c.errs) > 0 {
		err, c.errs = c.errs[0], c.errs[1:]
	}
	gate := c.gate
	c.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return c.InProcessClient.Send(ctx, req, stream, onReceipt)
}

func (c *faultyChunkClient) sent() [][2]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]uint64(nil), c.sends...)
}

func fastChunkRetry(t *testing.T) {
	t.Helper()
	base, maxDelay := catchUpChunkRetryBase, catchUpChunkRetryMax
	catchUpChunkRetryBase, catchUpChunkRetryMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { catchUpChunkRetryBase, catchUpChunkRetryMax = base, maxDelay })
}

func wrapFaulty(session *Session, hostIdx int, errs ...error) *faultyChunkClient {
	c := &faultyChunkClient{InProcessClient: session.clients[hostIdx].(*InProcessClient), errs: errs}
	session.clients[hostIdx] = c
	return c
}

func hostCursor(session *Session, hostIdx int) uint64 {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.hostSyncNonce[hostIdx]
}

func status500() error {
	return &transport.UpstreamStatusError{StatusCode: http.StatusInternalServerError, Body: "internal"}
}

// Test flow:
//  1. Every try of the first chunk gets a 500.
//  2. The catch-up fails and the host's cursor does not move.
//  3. The next catch-up resends the same nonces and the cursor reaches the tip.
func TestCatchUp_FailedChunkKeepsCursorAndIsResent(t *testing.T) {
	fastChunkRetry(t)
	session := setupStoredSession(t, storage.NewMemory())
	const n = catchUpChunkSize + 20
	composeEmptyDiffs(t, session, n)

	errs := make([]error, catchUpChunkAttempts)
	for i := range errs {
		errs[i] = status500()
	}
	client := wrapFaulty(session, 0, errs...)

	require.Error(t, session.sendCatchUpThrough(context.Background(), 0, client, n))
	require.Zero(t, hostCursor(session, 0), "no 200 OK, no cursor move")
	sends := client.sent()
	require.Len(t, sends, catchUpChunkAttempts)
	for _, s := range sends {
		require.Equal(t, [2]uint64{1, catchUpChunkSize}, s, "each try resends the same chunk")
	}

	require.NoError(t, session.sendCatchUpThrough(context.Background(), 0, client, n))
	require.Equal(t, uint64(n), hostCursor(session, 0))
	require.Equal(t, [][2]uint64{{1, catchUpChunkSize}, {catchUpChunkSize + 1, n}}, client.sent()[catchUpChunkAttempts:])
}

func TestCatchUp_StorageBusyChunkIsResentInPlace(t *testing.T) {
	fastChunkRetry(t)
	session := setupStoredSession(t, storage.NewMemory())
	const n = 50
	composeEmptyDiffs(t, session, n)

	busy := &transport.UpstreamStatusError{StatusCode: http.StatusServiceUnavailable, DevshardError: transport.DevshardErrorStorageBusy}
	client := wrapFaulty(session, 0, busy, busy)

	require.NoError(t, session.sendCatchUpThrough(context.Background(), 0, client, n))
	require.Equal(t, uint64(n), hostCursor(session, 0))
	require.Equal(t, [][2]uint64{{1, n}, {1, n}, {1, n}}, client.sent())
}

func TestCatchUp_DivergenceIsNotResent(t *testing.T) {
	fastChunkRetry(t)
	session := setupStoredSession(t, storage.NewMemory())
	composeEmptyDiffs(t, session, 10)

	conflict := &transport.UpstreamStatusError{StatusCode: http.StatusConflict, Body: "post-state root mismatch"}
	client := wrapFaulty(session, 0, conflict)

	require.Error(t, session.sendCatchUpThrough(context.Background(), 0, client, 10))
	require.Len(t, client.sent(), 1, "a 4xx fails the same way on a resend")
	require.Zero(t, hostCursor(session, 0))
}

func TestCatchUp_CanceledChunkKeepsCursorAndIsResent(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = 30
	composeEmptyDiffs(t, session, n)

	client := wrapFaulty(session, 0)
	client.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	require.Error(t, session.sendCatchUpThrough(ctx, 0, client, n))
	require.Zero(t, hostCursor(session, 0))

	close(client.gate)
	require.NoError(t, session.sendCatchUpThrough(context.Background(), 0, client, n))
	require.Equal(t, uint64(n), hostCursor(session, 0))
	require.Equal(t, [][2]uint64{{1, n}, {1, n}}, client.sent())
}

// Test flow:
//  1. A send to a far-behind host starts a chunked catch-up held at the host.
//  2. The attempt is canceled; it stops waiting.
//  3. The catch-up keeps running, is reported by HostCatchingUp, and finishes.
func TestCatchUpForSend_OutlivesTheAttempt(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = catchUpChunkSize + 50
	composeEmptyDiffs(t, session, n)

	client := wrapFaulty(session, 1)
	client.gate = make(chan struct{})

	attemptCtx, cancelAttempt := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.catchUpForSend(attemptCtx, 1, n)
		done <- err
	}()
	require.Eventually(t, func() bool { return session.HostCatchingUp(1) }, 2*time.Second, 5*time.Millisecond)
	require.True(t, session.OtherHostReady(1))
	require.False(t, session.HostCatchingUp(0))

	cancelAttempt()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("canceled attempt kept waiting for the catch-up")
	}
	require.True(t, session.HostCatchingUp(1), "the catch-up is not tied to the attempt")

	close(client.gate)
	require.Eventually(t, func() bool {
		return !session.HostCatchingUp(1) && hostCursor(session, 1) == n-1
	}, 5*time.Second, 5*time.Millisecond)
}

// Test flow:
//  1. A catch-up to host 1 is in flight, held at the host.
//  2. Ten more sends ask for catch-ups with rising targets.
//  3. They all join one queued run aimed at the highest target, so the host
//     sees two catch-ups, not eleven.
func TestStartHostCatchUp_SendsWhileOneIsInFlightShareTheNextRun(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	composeEmptyDiffs(t, session, 40)

	client := wrapFaulty(session, 1)
	client.gate = make(chan struct{})

	first := session.startHostCatchUp(1, 10)
	require.Eventually(t, func() bool { return len(client.sent()) == 1 }, 2*time.Second, 5*time.Millisecond)

	next := session.startHostCatchUp(1, 20)
	require.NotSame(t, first, next)
	for i := uint64(1); i < 10; i++ {
		require.Same(t, next, session.startHostCatchUp(1, 20+i))
	}

	close(client.gate)
	for _, run := range []*hostCatchUpRun{first, next} {
		select {
		case <-run.done:
			require.NoError(t, run.err)
		case <-time.After(5 * time.Second):
			t.Fatal("catch-up did not finish")
		}
	}
	require.Equal(t, [][2]uint64{{1, 10}, {11, 29}}, client.sent())
	require.Equal(t, uint64(29), hostCursor(session, 1))
	require.Eventually(t, func() bool { return !session.HostCatchingUp(1) }, 2*time.Second, 5*time.Millisecond)
}

func TestProcessResponse_CursorRules(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = 20
	roots := composeEmptyDiffs(t, session, n)

	session.mu.Lock()
	defer session.mu.Unlock()

	// A verified hash proves the host holds resp.Nonce, even past the request.
	require.NoError(t, session.processResponse(0, &host.HostResponse{Nonce: 12, StateHash: roots[12]}, 8))
	require.Equal(t, uint64(12), session.hostSyncNonce[0])

	// A receipt from a stream that broke later proves only its own hash.
	require.NoError(t, session.processResponse(1, &host.HostResponse{Nonce: 15, StateHash: roots[15], Receipt: []byte("r")}, 15))
	require.Equal(t, uint64(15), session.hostSyncNonce[1], "the cursor stops at the receipt's proven nonce")

	// Without a hash the cursor stops at what the request carried.
	require.NoError(t, session.processResponse(2, &host.HostResponse{Nonce: n}, 5))
	require.Equal(t, uint64(5), session.hostSyncNonce[2])

	// A wrong hash moves nothing.
	require.Error(t, session.processResponse(2, &host.HostResponse{Nonce: 18, StateHash: []byte("not-the-root")}, 18))
	require.Equal(t, uint64(5), session.hostSyncNonce[2])
}
