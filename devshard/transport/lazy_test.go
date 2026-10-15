package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func expireLazyFailure[T any](l *Lazy[T]) {
	l.mu.Lock()
	l.retryAt = time.Now().Add(-time.Second)
	l.mu.Unlock()
}

// lazyBuilds counts builds of an int and blocks each one on gate when set.
type lazyBuilds struct {
	calls    atomic.Int32
	err      atomic.Pointer[error]
	gate     chan struct{}
	released atomic.Int32
}

func (b *lazyBuilds) build() (int, error) {
	n := int(b.calls.Add(1))
	if b.gate != nil {
		<-b.gate
	}
	if p := b.err.Load(); p != nil {
		return 0, *p
	}
	return n, nil
}

func (b *lazyBuilds) release(int) { b.released.Add(1) }

func (b *lazyBuilds) fail(err error) { b.err.Store(&err) }

// Tests that a built value is kept and handed to every later caller.
//  1. Get twice.
//  2. Expect one build and the same value both times.
func TestLazy_KeepsBuiltValue(t *testing.T) {
	b := &lazyBuilds{}
	l := NewLazy(b.build, b.release, 0, 0)

	for i := 0; i < 2; i++ {
		v, err := l.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, v)
	}
	require.EqualValues(t, 1, b.calls.Load())
	v, ok := l.Peek()
	require.True(t, ok)
	require.Equal(t, 1, v)
}

// Tests that a failed build answers until the retry window ends.
//  1. Fail the build and Get twice; Start inside the window too.
//  2. Expire the window and let the build succeed.
//  3. Expect one build for the first calls, Failure reporting it, and the value after the window.
func TestLazy_FailureHeldForRetryWindow(t *testing.T) {
	b := &lazyBuilds{}
	cause := errors.New("chain down")
	b.fail(cause)
	l := NewLazy(b.build, b.release, 0, time.Hour)

	for i := 0; i < 2; i++ {
		_, err := l.Get(context.Background())
		require.ErrorIs(t, err, cause)
	}
	l.Start()
	require.EqualValues(t, 1, b.calls.Load())
	retryAt, err := l.Failure()
	require.ErrorIs(t, err, cause)
	require.True(t, retryAt.After(time.Now()))

	expireLazyFailure(l)
	b.err.Store(nil)
	v, err := l.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, v)
	_, err = l.Failure()
	require.NoError(t, err, "a held value clears the failure")
}

// Tests that concurrent callers share one build.
//  1. Block the build and Get from eight goroutines.
//  2. Release the build.
//  3. Expect one build and every caller to get its value.
func TestLazy_ConcurrentCallersShareOneBuild(t *testing.T) {
	b := &lazyBuilds{gate: make(chan struct{})}
	l := NewLazy(b.build, b.release, 0, 0)

	type result struct {
		v   int
		err error
	}
	const callers = 8
	got := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := l.Get(context.Background())
			got <- result{v: v, err: err}
		}()
	}
	require.Eventually(t, func() bool { return b.calls.Load() == 1 }, time.Second, time.Millisecond)
	close(b.gate)
	wg.Wait()
	close(got)

	require.EqualValues(t, 1, b.calls.Load())
	for res := range got {
		require.NoError(t, res.err)
		require.Equal(t, 1, res.v)
	}
}

// Tests that a hung build holds a caller no longer than its context or the wait bound.
//  1. Block the build and Get with a short context, then with no deadline under a short wait bound.
//  2. Expect the context error, then ErrLazyTimeout.
//  3. Release the build and expect the value to be kept for the next caller.
func TestLazy_HungBuildIsBounded(t *testing.T) {
	b := &lazyBuilds{gate: make(chan struct{})}
	l := NewLazy(b.build, b.release, 20*time.Millisecond, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := l.Get(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = l.Get(context.Background())
	require.ErrorIs(t, err, ErrLazyTimeout)
	require.EqualValues(t, 1, b.calls.Load(), "waiters join the running build")

	close(b.gate)
	require.Eventually(t, func() bool {
		_, ok := l.Peek()
		return ok
	}, time.Second, time.Millisecond)
}

// Tests that Start builds without a caller waiting.
//  1. Start, then Start again while the build is blocked.
//  2. Release the build.
//  3. Expect one build and the value held.
func TestLazy_StartBuildsInBackground(t *testing.T) {
	b := &lazyBuilds{gate: make(chan struct{})}
	l := NewLazy(b.build, b.release, 0, 0)

	l.Start()
	l.Start()
	require.Eventually(t, func() bool { return b.calls.Load() == 1 }, time.Second, time.Millisecond)
	_, ok := l.Peek()
	require.False(t, ok)

	close(b.gate)
	require.Eventually(t, func() bool {
		_, ok := l.Peek()
		return ok
	}, time.Second, time.Millisecond)
	require.EqualValues(t, 1, b.calls.Load())
}

// Tests that Close releases a held value once and a build finishing after Close is released.
//  1. Close a built Lazy twice; expect one release and ErrLazyClosed on Get.
//  2. Close a Lazy whose build is blocked, then release the build.
//  3. Expect the waiter to get ErrLazyClosed and the late value to be released.
func TestLazy_CloseReleases(t *testing.T) {
	b := &lazyBuilds{}
	l := NewLazy(b.build, b.release, 0, 0)
	_, err := l.Get(context.Background())
	require.NoError(t, err)
	l.Close()
	l.Close()
	require.EqualValues(t, 1, b.released.Load())
	_, err = l.Get(context.Background())
	require.ErrorIs(t, err, ErrLazyClosed)

	late := &lazyBuilds{gate: make(chan struct{})}
	pending := NewLazy(late.build, late.release, 0, 0)
	done := make(chan error, 1)
	go func() {
		_, err := pending.Get(context.Background())
		done <- err
	}()
	require.Eventually(t, func() bool { return late.calls.Load() == 1 }, time.Second, time.Millisecond)
	pending.Close()
	close(late.gate)
	require.ErrorIs(t, <-done, ErrLazyClosed)
	require.Eventually(t, func() bool { return late.released.Load() == 1 }, time.Second, time.Millisecond)
	_, ok := pending.Peek()
	require.False(t, ok)
}
