package nodemanager_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"common/nodemanager"
	"common/nodemanager/gen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubLock is a test double for nodemanager.NodeLock.
type stubLock struct {
	mu         sync.Mutex
	acquires   []acquireResult
	acquireIdx int
	releases   []releaseCall
	releaseErr error
}

type acquireResult struct {
	nodeID   string
	endpoint string
	lockID   string
	err      error
}

type releaseCall struct {
	lockID  string
	outcome gen.ReleaseOutcome
	ctxDone bool // was ctx already cancelled when Release was called?
}

func (s *stubLock) Acquire(_ context.Context, _ string, _ []string) (*gen.AcquireMLNodeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acquireIdx >= len(s.acquires) {
		return nil, fmt.Errorf("stub: no more acquire results configured")
	}
	r := s.acquires[s.acquireIdx]
	s.acquireIdx++
	if r.err != nil {
		return nil, r.err
	}
	return &gen.AcquireMLNodeResponse{LockId: r.lockID, Endpoint: r.endpoint, NodeId: r.nodeID}, nil
}

func (s *stubLock) Release(ctx context.Context, lockID string, outcome gen.ReleaseOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = append(s.releases, releaseCall{
		lockID:  lockID,
		outcome: outcome,
		ctxDone: ctx.Err() != nil,
	})
	return s.releaseErr
}

// trackingBody records whether Close was called.
type trackingBody struct {
	io.Reader
	mu     sync.Mutex
	closed bool
}

func (b *trackingBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *trackingBody) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func okBody() io.ReadCloser {
	return io.NopCloser(strings.NewReader("ok"))
}

// --- Tests ---

func TestDoWithNode_Success(t *testing.T) {
	lock := &stubLock{
		acquires: []acquireResult{{nodeID: "node-1", endpoint: "http://node1", lockID: "lock-1"}},
	}

	resp, err := nodemanager.DoWithNode(context.Background(), lock, "model-a", 3,
		func(_ context.Context, endpoint string) (*http.Response, error) {
			assert.Equal(t, "http://node1", endpoint)
			return &http.Response{StatusCode: http.StatusOK, Body: okBody()}, nil
		},
	)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, lock.releases, 1)
	assert.Equal(t, gen.ReleaseOutcome_SUCCESS, lock.releases[0].outcome)
	assert.Equal(t, "lock-1", lock.releases[0].lockID)
}

func TestDoWithNode_TransportError_Retries_ExcludesNode(t *testing.T) {
	lock := &stubLock{
		acquires: []acquireResult{
			{nodeID: "node-1", endpoint: "http://node1", lockID: "lock-1"},
			{nodeID: "node-2", endpoint: "http://node2", lockID: "lock-2"},
		},
	}

	attempts := 0
	resp, err := nodemanager.DoWithNode(context.Background(), lock, "model-a", 3,
		func(_ context.Context, _ string) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, fmt.Errorf("connection refused")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: okBody()}, nil
		},
	)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, attempts)
	require.Len(t, lock.releases, 2)
	assert.Equal(t, gen.ReleaseOutcome_TRANSPORT_ERROR, lock.releases[0].outcome)
	assert.Equal(t, gen.ReleaseOutcome_SUCCESS, lock.releases[1].outcome)
}

func TestDoWithNode_5xx_ClosesBodyBeforeRetry(t *testing.T) {
	lock := &stubLock{
		acquires: []acquireResult{
			{nodeID: "node-1", endpoint: "http://node1", lockID: "lock-1"},
			{nodeID: "node-2", endpoint: "http://node2", lockID: "lock-2"},
		},
	}

	firstBody := &trackingBody{Reader: strings.NewReader("error body")}
	attempts := 0

	resp, err := nodemanager.DoWithNode(context.Background(), lock, "model-a", 2,
		func(_ context.Context, _ string) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{StatusCode: http.StatusInternalServerError, Body: firstBody}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: okBody()}, nil
		},
	)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, firstBody.isClosed(), "5xx response body must be closed before retry to avoid connection leak")
}

func TestDoWithNode_4xx_NoRetry(t *testing.T) {
	lock := &stubLock{
		acquires: []acquireResult{{nodeID: "node-1", endpoint: "http://node1", lockID: "lock-1"}},
	}

	attempts := 0
	_, err := nodemanager.DoWithNode(context.Background(), lock, "model-a", 3,
		func(_ context.Context, _ string) (*http.Response, error) {
			attempts++
			return &http.Response{StatusCode: http.StatusBadRequest, Body: okBody()}, nil
		},
	)

	require.Error(t, err)
	assert.Equal(t, 1, attempts, "4xx should not be retried")
	require.Len(t, lock.releases, 1)
	assert.Equal(t, gen.ReleaseOutcome_APPLICATION_ERROR, lock.releases[0].outcome)
}

func TestDoWithNode_Timeout_StopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lock := &stubLock{
		acquires: []acquireResult{{nodeID: "node-1", endpoint: "http://node1", lockID: "lock-1"}},
	}

	attempts := 0
	_, err := nodemanager.DoWithNode(ctx, lock, "model-a", 3,
		func(_ context.Context, _ string) (*http.Response, error) {
			attempts++
			cancel()
			return nil, context.Canceled
		},
	)

	require.Error(t, err)
	assert.Equal(t, 1, attempts, "timeout should not be retried")
	require.Len(t, lock.releases, 1)
	assert.Equal(t, gen.ReleaseOutcome_TIMEOUT, lock.releases[0].outcome)
}

// TestDoWithNode_Timeout_ReleaseUsesDetachedContext verifies that Release is called
// with a fresh context, not the original cancelled one. If the same cancelled context
// is reused, the gRPC Release call fails immediately and the server never frees the lock.
func TestDoWithNode_Timeout_ReleaseUsesDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lock := &stubLock{
		acquires: []acquireResult{{nodeID: "node-1", endpoint: "http://node1", lockID: "lock-1"}},
	}

	_, err := nodemanager.DoWithNode(ctx, lock, "model-a", 1,
		func(_ context.Context, _ string) (*http.Response, error) {
			cancel() // cancel before returning, simulating a timeout
			return nil, context.Canceled
		},
	)

	require.Error(t, err)
	require.Len(t, lock.releases, 1)
	assert.False(t, lock.releases[0].ctxDone,
		"Release must use a detached context — using the cancelled ctx causes the gRPC call to fail and leaks the server-side lock until TTL expiry")
}

// TestDoWithNode_ExhaustedAttempts_ReturnsLastError verifies that the caller gets
// the actual error from the last attempt, not a generic "all attempts failed" message.
func TestDoWithNode_ExhaustedAttempts_ReturnsLastError(t *testing.T) {
	lock := &stubLock{
		acquires: []acquireResult{
			{nodeID: "node-1", endpoint: "http://node1", lockID: "lock-1"},
			{nodeID: "node-2", endpoint: "http://node2", lockID: "lock-2"},
		},
	}

	attempt := 0
	_, err := nodemanager.DoWithNode(context.Background(), lock, "model-a", 2,
		func(_ context.Context, _ string) (*http.Response, error) {
			attempt++
			return nil, fmt.Errorf("specific failure on attempt %d", attempt)
		},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "specific failure on attempt 2",
		"exhausted error should wrap the last actual error, not a generic message")
}

func TestDoWithNode_AcquireError_ReturnsImmediately(t *testing.T) {
	lock := &stubLock{
		acquires: []acquireResult{{err: fmt.Errorf("no nodes available")}},
	}

	called := false
	_, err := nodemanager.DoWithNode(context.Background(), lock, "model-a", 3,
		func(_ context.Context, _ string) (*http.Response, error) {
			called = true
			return &http.Response{StatusCode: http.StatusOK, Body: okBody()}, nil
		},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "acquire node")
	assert.False(t, called, "do should not be called when acquire fails")
	assert.Empty(t, lock.releases, "Release should not be called when Acquire fails")
}

func TestDoWithNode_ReleaseError_NonFatal(t *testing.T) {
	lock := &stubLock{
		acquires:   []acquireResult{{endpoint: "http://node1", lockID: "lock-1"}},
		releaseErr: fmt.Errorf("release failed"),
	}

	resp, err := nodemanager.DoWithNode(context.Background(), lock, "model-a", 1,
		func(_ context.Context, _ string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: okBody()}, nil
		},
	)

	require.NoError(t, err, "Release error must not surface to the caller")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
