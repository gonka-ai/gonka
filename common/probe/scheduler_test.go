package probe_test

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/probe"

	"github.com/stretchr/testify/require"
)

type memSink struct {
	mu        sync.Mutex
	observed  []probe.Result
	forgotten []string
}

func (m *memSink) Observe(r probe.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observed = append(m.observed, r)
}

func (m *memSink) Forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgotten = append(m.forgotten, key)
}

type obsCounters struct {
	started atomic.Int32
	skipped atomic.Int32
	count   atomic.Int32
}

func (o *obsCounters) TickStarted()        { o.started.Add(1) }
func (o *obsCounters) TickSkipped()        { o.skipped.Add(1) }
func (o *obsCounters) TargetCount(n int)   { o.count.Store(int32(n)) }

type staticSource struct {
	mu   sync.Mutex
	list []probe.Target
}

func (s *staticSource) Targets() []probe.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]probe.Target, len(s.list))
	copy(out, s.list)
	return out
}

func (s *staticSource) set(list []probe.Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = list
}

func TestScheduler_SkipsOverlappingTick(t *testing.T) {
	release := make(chan struct{})
	var concurrent atomic.Int32
	var maxConc atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cur := concurrent.Add(1)
		for {
			prev := maxConc.Load()
			if cur <= prev || maxConc.CompareAndSwap(prev, cur) {
				break
			}
		}
		<-release
		concurrent.Add(-1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})

	p, err := probe.New(probe.Config{
		Interval:    40 * time.Millisecond,
		Timeout:     20 * time.Millisecond,
		Concurrency: 2,
		Transport:   rt,
	})
	require.NoError(t, err)

	src := &staticSource{list: []probe.Target{
		{Key: "a", FallbackURL: "http://x/a"},
		{Key: "b", FallbackURL: "http://x/b"},
		{Key: "c", FallbackURL: "http://x/c"},
		{Key: "d", FallbackURL: "http://x/d"},
	}}
	sink := &memSink{}
	obs := &obsCounters{}
	sched := probe.NewScheduler(p, src, sink, obs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	// Wait until first tick is in flight (workers blocked on release).
	require.Eventually(t, func() bool { return obs.started.Load() >= 1 }, time.Second, 5*time.Millisecond)
	// Let a few ticker fires happen while still in flight.
	time.Sleep(120 * time.Millisecond)
	require.Greater(t, obs.skipped.Load(), int32(0), "expected skipped ticks while in flight")
	require.LessOrEqual(t, maxConc.Load(), int32(2), "concurrency cap")

	close(release)
	cancel()
}

func TestScheduler_SnapshotSemantics(t *testing.T) {
	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	p, err := probe.New(probe.Config{
		Interval:    time.Hour, // won't fire again
		Timeout:     time.Second,
		Concurrency: 4,
		Transport:   rt,
	})
	require.NoError(t, err)

	src := &staticSource{list: []probe.Target{
		{Key: "a", FallbackURL: "http://x/a"},
		{Key: "b", FallbackURL: "http://x/b"},
	}}
	sink := &memSink{}
	sched := probe.NewScheduler(p, src, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	<-entered
	// Mutate source mid-tick; wave should still observe only the snapshotted two.
	src.set([]probe.Target{
		{Key: "a", FallbackURL: "http://x/a"},
		{Key: "b", FallbackURL: "http://x/b"},
		{Key: "c", FallbackURL: "http://x/c"},
	})
	close(block)

	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.observed) == 2
	}, time.Second, 5*time.Millisecond)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.observed, 2)
	cancel()
}

func TestScheduler_ForgetRemoved(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	p, err := probe.New(probe.Config{
		Interval: 40 * time.Millisecond, Timeout: 20 * time.Millisecond, Transport: rt,
	})
	require.NoError(t, err)
	src := &staticSource{list: []probe.Target{{Key: "a", FallbackURL: "http://x/a"}}}
	sink := &memSink{}
	sched := probe.NewScheduler(p, src, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.observed) >= 1
	}, time.Second, 5*time.Millisecond)

	src.set(nil)
	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.forgotten) >= 1
	}, time.Second, 5*time.Millisecond)
	cancel()
}
