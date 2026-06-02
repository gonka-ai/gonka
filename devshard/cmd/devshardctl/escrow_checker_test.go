package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/transport"
)

func TestIsUpstreamEscrowNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
		{
			name: "upstream 500 with escrow not found",
			err: &transport.UpstreamStatusError{
				Path:       "/sessions/1/chat/completions",
				StatusCode: http.StatusInternalServerError,
				Body:       `{"error":"build group: get escrow: escrow not found"}`,
			},
			want: true,
		},
		{
			name: "upstream 500 without escrow message",
			err: &transport.UpstreamStatusError{
				Path:       "/sessions/1/chat/completions",
				StatusCode: http.StatusInternalServerError,
				Body:       `{"error":"internal server error"}`,
			},
			want: false,
		},
		{
			name: "upstream 429 with escrow not found",
			err: &transport.UpstreamStatusError{
				Path:       "/sessions/1/chat/completions",
				StatusCode: http.StatusTooManyRequests,
				Body:       `{"error":"escrow not found"}`,
			},
			want: false,
		},
		{
			name: "wrapped upstream 500 with escrow not found",
			err: fmt.Errorf("send to host: %w", &transport.UpstreamStatusError{
				Path:       "/sessions/1/chat/completions",
				StatusCode: http.StatusInternalServerError,
				Body:       `{"error":"build group: get escrow: escrow not found"}`,
			}),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, transport.IsUpstreamEscrowNotFound(tt.err))
		})
	}
}

func TestEscrowCheckerDeduplicates(t *testing.T) {
	var chainCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chainCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"found": false}`)
	}))
	defer srv.Close()

	checker := NewEscrowChecker(func() string { return srv.URL })
	var deactivated atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checker.TriggerCheck("42", func() {
				deactivated.Add(1)
			})
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), chainCalls.Load(), "should make exactly one chain call")
	assert.Equal(t, int64(1), deactivated.Load(), "should deactivate exactly once")
}

func TestEscrowCheckerKeepsActiveWhenFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"escrow":{"id":"42","creator":"addr","amount":"1000","slots":["a","b"],"epoch_index":"0","app_hash":"","token_price":"1"},"found":true}`)
	}))
	defer srv.Close()

	checker := NewEscrowChecker(func() string { return srv.URL })
	var deactivated atomic.Int64

	checker.TriggerCheck("42", func() {
		deactivated.Add(1)
	})

	assert.Equal(t, int64(0), deactivated.Load(), "should not deactivate when escrow exists")
}

func TestEscrowCheckerKeepsActiveOnChainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	checker := NewEscrowChecker(func() string { return srv.URL })
	var deactivated atomic.Int64

	checker.TriggerCheck("42", func() {
		deactivated.Add(1)
	})

	assert.Equal(t, int64(0), deactivated.Load(), "should not deactivate on chain error")
}

func TestRedundancyCallsEscrowMissing(t *testing.T) {
	var called atomic.Int64
	r := &Redundancy{
		devshardID: "test-escrow",
		onEscrowMissing: func() {
			called.Add(1)
		},
	}

	attempts := []*inflight{
		{
			hostID: "host-a",
			nonce:  1,
			err: &transport.UpstreamStatusError{
				Path:       "/sessions/test-escrow/chat/completions",
				StatusCode: http.StatusInternalServerError,
				Body:       `{"error":"build group: get escrow: escrow not found"}`,
			},
			done: closedChan(),
		},
		{
			hostID: "host-b",
			nonce:  2,
			err:    nil,
			done:   closedChan(),
		},
	}

	ctx := mustRequestLogContext()
	r.checkEscrowMissing(ctx, attempts)
	require.Equal(t, int64(1), called.Load())
}

func TestRedundancyNoCallbackWithoutEscrowError(t *testing.T) {
	var called atomic.Int64
	r := &Redundancy{
		devshardID: "test-escrow",
		onEscrowMissing: func() {
			called.Add(1)
		},
	}

	attempts := []*inflight{
		{
			hostID: "host-a",
			nonce:  1,
			err:    fmt.Errorf("connection refused"),
			done:   closedChan(),
		},
	}

	ctx := mustRequestLogContext()
	r.checkEscrowMissing(ctx, attempts)
	require.Equal(t, int64(0), called.Load())
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func mustRequestLogContext() context.Context {
	ctx, _ := ensureRequestLogContext(context.Background())
	return ctx
}
