package transport

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// DefaultLazyWait bounds how long one Get waits for a build. A build that
	// outlives the wait keeps running and fills the value for the next caller.
	DefaultLazyWait = 5 * time.Second
	// DefaultLazyRetry is how long a failed build answers before the next one
	// runs. Transient chain errors are not cached below this layer, so without
	// it every caller would query the chain.
	DefaultLazyRetry = 10 * time.Second
)

var (
	ErrLazyClosed  = errors.New("lazy client closed")
	ErrLazyTimeout = errors.New("lazy client build timed out")
)

// Lazy is a client built in the background on first use and kept once
// built. Concurrent callers share one build. A failed build answers for the
// retry window before another runs. A build that finishes after Close is
// released, not kept.
type Lazy[T any] struct {
	build   func() (T, error)
	release func(T)
	wait    time.Duration
	retry   time.Duration

	mu      sync.Mutex
	val     T
	ok      bool
	pending chan struct{}
	err     error
	retryAt time.Time
	closed  bool
}

// NewLazy returns an unbuilt Lazy. release may be nil. wait and retry fall
// back to DefaultLazyWait and DefaultLazyRetry when not positive.
func NewLazy[T any](build func() (T, error), release func(T), wait, retry time.Duration) *Lazy[T] {
	if wait <= 0 {
		wait = DefaultLazyWait
	}
	if retry <= 0 {
		retry = DefaultLazyRetry
	}
	return &Lazy[T]{build: build, release: release, wait: wait, retry: retry}
}

// ReadyLazy returns a Lazy already holding v.
func ReadyLazy[T any](v T, release func(T)) *Lazy[T] {
	return &Lazy[T]{val: v, ok: true, release: release, wait: DefaultLazyWait, retry: DefaultLazyRetry}
}

// Get returns the value, starting a build when none is held, running, or
// inside the retry window. It waits for the build until it finishes, ctx
// ends, or the wait bound passes.
func (l *Lazy[T]) Get(ctx context.Context) (T, error) {
	var zero T
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return zero, ErrLazyClosed
	}
	if l.ok {
		v := l.val
		l.mu.Unlock()
		return v, nil
	}
	if l.pending == nil {
		if l.err != nil && time.Now().Before(l.retryAt) {
			err := l.err
			l.mu.Unlock()
			return zero, err
		}
		l.startLocked()
	}
	done := l.pending
	l.mu.Unlock()

	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-timer.C:
		return zero, ErrLazyTimeout
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.closed:
		return zero, ErrLazyClosed
	case l.ok:
		return l.val, nil
	default:
		return zero, l.err
	}
}

// Start begins a build without waiting for it. No-op when a value is held,
// a build is running, the retry window is open, or l is closed.
func (l *Lazy[T]) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.ok || l.pending != nil {
		return
	}
	if l.err != nil && time.Now().Before(l.retryAt) {
		return
	}
	l.startLocked()
}

// Peek returns the value without building it.
func (l *Lazy[T]) Peek() (T, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || !l.ok {
		var zero T
		return zero, false
	}
	return l.val, true
}

// Failure is when the next build may run and the last build error. err is
// nil while no build has failed, or once a value is held.
func (l *Lazy[T]) Failure() (retryAt time.Time, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ok {
		return time.Time{}, nil
	}
	return l.retryAt, l.err
}

// Close releases a held value and makes later Gets fail with ErrLazyClosed.
func (l *Lazy[T]) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	v, ok := l.val, l.ok
	var zero T
	l.val, l.ok = zero, false
	l.mu.Unlock()
	if ok && l.release != nil {
		l.release(v)
	}
}

func (l *Lazy[T]) startLocked() {
	done := make(chan struct{})
	l.pending = done
	go l.run(done)
}

func (l *Lazy[T]) run(done chan struct{}) {
	v, err := l.build()
	l.mu.Lock()
	l.pending = nil
	closed := l.closed
	switch {
	case closed:
	case err != nil:
		l.err = err
		l.retryAt = time.Now().Add(l.retry)
	default:
		l.val, l.ok, l.err = v, true, nil
	}
	l.mu.Unlock()
	close(done)
	if closed && err == nil && l.release != nil {
		l.release(v)
	}
}
