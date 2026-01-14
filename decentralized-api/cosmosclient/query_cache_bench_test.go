package cosmosclient

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type benchMockConn struct {
	callCount atomic.Int32
	delay     time.Duration
}

func (m *benchMockConn) Invoke(ctx context.Context, method string, args any, reply any, opts ...grpc.CallOption) error {
	m.callCount.Add(1)
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if msg, ok := reply.(*wrapperspb.StringValue); ok {
		msg.Value = "response"
	}
	return nil
}

func (m *benchMockConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func BenchmarkCache_Hit(b *testing.B) {
	ClearCache()
	mock := &benchMockConn{}
	conn := NewCachingConn(mock)

	SetCacheable("/bench.Query/Cached", true)
	defer SetCacheable("/bench.Query/Cached", false)

	req := &wrapperspb.StringValue{Value: "request"}
	conn.Invoke(context.Background(), "/bench.Query/Cached", req, &wrapperspb.StringValue{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn.Invoke(context.Background(), "/bench.Query/Cached", req, &wrapperspb.StringValue{})
	}
}

func BenchmarkCache_Miss(b *testing.B) {
	ClearCache()
	mock := &benchMockConn{}
	conn := NewCachingConn(mock)

	req := &wrapperspb.StringValue{Value: "request"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn.Invoke(context.Background(), "/bench.Query/Uncached", req, &wrapperspb.StringValue{})
	}
}

func BenchmarkCache_Store(b *testing.B) {
	mock := &benchMockConn{}
	conn := NewCachingConn(mock)

	SetCacheable("/bench.Query/Store", true)
	defer SetCacheable("/bench.Query/Store", false)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ClearCache()
		req := &wrapperspb.StringValue{Value: fmt.Sprintf("req-%d", i)}
		conn.Invoke(context.Background(), "/bench.Query/Store", req, &wrapperspb.StringValue{})
	}
}

func BenchmarkCache_WithLatency(b *testing.B) {
	b.Run("NoCache_1ms", func(b *testing.B) {
		ClearCache()
		mock := &benchMockConn{delay: 1 * time.Millisecond}
		conn := NewCachingConn(mock)
		req := &wrapperspb.StringValue{Value: "request"}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			conn.Invoke(context.Background(), "/bench.Query/Uncached", req, &wrapperspb.StringValue{})
		}
	})

	b.Run("WithCache_1ms", func(b *testing.B) {
		ClearCache()
		mock := &benchMockConn{delay: 1 * time.Millisecond}
		conn := NewCachingConn(mock)

		SetCacheable("/bench.Query/Latency", true)
		defer SetCacheable("/bench.Query/Latency", false)

		req := &wrapperspb.StringValue{Value: "request"}
		conn.Invoke(context.Background(), "/bench.Query/Latency", req, &wrapperspb.StringValue{})

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			conn.Invoke(context.Background(), "/bench.Query/Latency", req, &wrapperspb.StringValue{})
		}
	})
}
