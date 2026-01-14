package cosmosclient

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type mockConn struct {
	callCount atomic.Int32
	delay     time.Duration
	onInvoke  func()
}

func (m *mockConn) Invoke(ctx context.Context, method string, args any, reply any, opts ...grpc.CallOption) error {
	m.callCount.Add(1)
	if m.onInvoke != nil {
		m.onInvoke()
	}
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if msg, ok := reply.(*wrapperspb.StringValue); ok {
		msg.Value = "response"
	}
	return nil
}

func (m *mockConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func TestCache_HitAndMiss(t *testing.T) {
	ClearCache()
	mock := &mockConn{}
	conn := NewCachingConn(mock)

	SetCacheable("/test.Query/Cached", true)
	defer SetCacheable("/test.Query/Cached", false)

	req := &wrapperspb.StringValue{Value: "request"}

	// First call - cache miss
	conn.Invoke(context.Background(), "/test.Query/Cached", req, &wrapperspb.StringValue{})
	// Second call - cache hit
	conn.Invoke(context.Background(), "/test.Query/Cached", req, &wrapperspb.StringValue{})

	if mock.callCount.Load() != 1 {
		t.Errorf("Expected 1 call (cache hit), got %d", mock.callCount.Load())
	}

	// Uncached method - always calls
	conn.Invoke(context.Background(), "/test.Query/Uncached", req, &wrapperspb.StringValue{})
	conn.Invoke(context.Background(), "/test.Query/Uncached", req, &wrapperspb.StringValue{})

	if mock.callCount.Load() != 3 {
		t.Errorf("Expected 3 calls total, got %d", mock.callCount.Load())
	}
}

func TestCache_ClearOnNewBlock(t *testing.T) {
	ClearCache()
	mock := &mockConn{}
	conn := NewCachingConn(mock)

	SetCacheable("/test.Query/Block", true)
	defer SetCacheable("/test.Query/Block", false)

	req := &wrapperspb.StringValue{Value: "request"}

	conn.Invoke(context.Background(), "/test.Query/Block", req, &wrapperspb.StringValue{})
	ClearCache()
	conn.Invoke(context.Background(), "/test.Query/Block", req, &wrapperspb.StringValue{})

	if mock.callCount.Load() != 2 {
		t.Errorf("Expected 2 calls after cache clear, got %d", mock.callCount.Load())
	}
}

func TestCache_LRUEviction(t *testing.T) {
	ClearCache()
	mock := &mockConn{}
	conn := NewCachingConn(mock)

	SetCacheable("/test.Query/LRU", true)
	defer SetCacheable("/test.Query/LRU", false)

	// Insert more than MaxCacheEntries
	for i := 0; i < MaxCacheEntries+100; i++ {
		req := &wrapperspb.StringValue{Value: fmt.Sprintf("request-%d", i)}
		conn.Invoke(context.Background(), "/test.Query/LRU", req, &wrapperspb.StringValue{})
	}

	if grpcCache.Len() > MaxCacheEntries {
		t.Errorf("Cache size %d exceeds limit %d", grpcCache.Len(), MaxCacheEntries)
	}
}

func TestCache_Singleflight(t *testing.T) {
	ClearCache()
	mock := &mockConn{delay: 50 * time.Millisecond}
	conn := NewCachingConn(mock)

	SetCacheable("/test.Query/SF", true)
	defer SetCacheable("/test.Query/SF", false)

	req := &wrapperspb.StringValue{Value: "request"}
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn.Invoke(context.Background(), "/test.Query/SF", req, &wrapperspb.StringValue{})
		}()
	}
	wg.Wait()

	if mock.callCount.Load() != 1 {
		t.Errorf("Expected 1 call (singleflight), got %d", mock.callCount.Load())
	}
}

func TestCache_ConsecutiveHit(t *testing.T) {
	ClearCache()
	mock := &mockConn{}
	conn := NewCachingConn(mock)

	SetCacheable("/test.Query/Hit", true)
	defer SetCacheable("/test.Query/Hit", false)

	req := &wrapperspb.StringValue{Value: "request"}

	conn.Invoke(context.Background(), "/test.Query/Hit", req, &wrapperspb.StringValue{})

	if mock.callCount.Load() != 1 {
		t.Errorf("Expected 1 call, got %d", mock.callCount.Load())
	}

	conn.Invoke(context.Background(), "/test.Query/Hit", req, &wrapperspb.StringValue{})

	if mock.callCount.Load() != 1 {
		t.Errorf("Expected 1 call (cache hit), got %d", mock.callCount.Load())
	}
}

func TestCache_ClearDuringFetch(t *testing.T) {
	ClearCache()
	invokeStarted := make(chan struct{})
	canFinish := make(chan struct{})

	mock := &mockConn{
		onInvoke: func() {
			close(invokeStarted)
			<-canFinish
		},
	}
	conn := NewCachingConn(mock)

	SetCacheable("/test.Query/Clear", true)
	defer SetCacheable("/test.Query/Clear", false)

	req := &wrapperspb.StringValue{Value: "request"}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn.Invoke(context.Background(), "/test.Query/Clear", req, &wrapperspb.StringValue{})
	}()

	<-invokeStarted
	ClearCache()
	close(canFinish)
	wg.Wait()

	mock2 := &mockConn{}
	conn2 := NewCachingConn(mock2)
	conn2.Invoke(context.Background(), "/test.Query/Clear", req, &wrapperspb.StringValue{})

	// Generation check prevents stale data: request started before ClearCache
	// should NOT cache its result, so second request must hit backend
	if mock2.callCount.Load() != 1 {
		t.Errorf("Expected fresh call after ClearCache during fetch, got %d", mock2.callCount.Load())
	}
}

func TestIsCacheable_ThreadSafe(t *testing.T) {
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(2)
		method := fmt.Sprintf("/test.Query/ThreadSafe%d", i)

		go func(m string) {
			defer wg.Done()
			SetCacheable(m, true)
		}(method)

		go func(m string) {
			defer wg.Done()
			_ = IsCacheable(m)
		}(method)
	}

	wg.Wait()

	// Cleanup
	for i := 0; i < 100; i++ {
		SetCacheable(fmt.Sprintf("/test.Query/ThreadSafe%d", i), false)
	}
}
