package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/transport"
)

type stubMainnetBridge struct {
	getEscrow func(escrowID string) (*bridge.EscrowInfo, error)
	delay     time.Duration
}

func (s *stubMainnetBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (s *stubMainnetBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}
func (s *stubMainnetBridge) OnSettlementFinalized(string) error { return bridge.ErrNotImplemented }
func (s *stubMainnetBridge) GetHostInfo(string) (*bridge.HostInfo, error) {
	return nil, bridge.ErrNotImplemented
}
func (s *stubMainnetBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}
func (s *stubMainnetBridge) VerifyWarmKey(string, string) (bool, error) {
	return false, bridge.ErrNotImplemented
}
func (s *stubMainnetBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

func (s *stubMainnetBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.getEscrow != nil {
		return s.getEscrow(escrowID)
	}
	return nil, bridge.ErrEscrowNotFound
}

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
	stub := &stubMainnetBridge{
		delay: 50 * time.Millisecond,
		getEscrow: func(string) (*bridge.EscrowInfo, error) {
			chainCalls.Add(1)
			return nil, bridge.ErrEscrowNotFound
		},
	}
	checker := NewEscrowChecker(func() bridge.MainnetBridge { return stub })
	var deactivated atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checker.TriggerCheck("42", func(string) {
				deactivated.Add(1)
			})
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), chainCalls.Load(), "should make exactly one chain call")
	assert.Equal(t, int64(1), deactivated.Load(), "should deactivate exactly once")
}

func TestEscrowCheckerKeepsActiveWhenFound(t *testing.T) {
	stub := &stubMainnetBridge{
		getEscrow: func(escrowID string) (*bridge.EscrowInfo, error) {
			return &bridge.EscrowInfo{EscrowID: escrowID}, nil
		},
	}
	checker := NewEscrowChecker(func() bridge.MainnetBridge { return stub })
	var deactivated atomic.Int64

	checker.TriggerCheck("42", func(string) {
		deactivated.Add(1)
	})

	assert.Equal(t, int64(0), deactivated.Load(), "should not deactivate when escrow exists")
}

func TestEscrowCheckerKeepsActiveOnChainError(t *testing.T) {
	stub := &stubMainnetBridge{
		getEscrow: func(string) (*bridge.EscrowInfo, error) {
			return nil, fmt.Errorf("service unavailable")
		},
	}
	checker := NewEscrowChecker(func() bridge.MainnetBridge { return stub })
	var deactivated atomic.Int64

	checker.TriggerCheck("42", func(string) {
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

func TestRedundancyCallsEscrowMissingOnSettled(t *testing.T) {
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
				StatusCode: http.StatusConflict,
				Body:       `{"message":"escrow already settled: escrow test-escrow"}`,
			},
			done: closedChan(),
		},
	}

	ctx := mustRequestLogContext()
	r.checkEscrowMissing(ctx, attempts)
	require.Equal(t, int64(1), called.Load())
}

func TestEscrowCheckerDeactivatesOnSettled(t *testing.T) {
	stub := &stubMainnetBridge{
		getEscrow: func(escrowID string) (*bridge.EscrowInfo, error) {
			return &bridge.EscrowInfo{EscrowID: escrowID, Settled: true}, nil
		},
	}
	checker := NewEscrowChecker(func() bridge.MainnetBridge { return stub })
	var reason string
	var deactivated atomic.Int64

	checker.TriggerCheck("42", func(r string) {
		reason = r
		deactivated.Add(1)
	})

	assert.Equal(t, int64(1), deactivated.Load(), "settled escrow must deactivate the devshard")
	assert.Contains(t, reason, "settled")
}

func TestIsUpstreamEscrowSettled(t *testing.T) {
	settled := &transport.UpstreamStatusError{
		Path:       "/sessions/1/chat/completions",
		StatusCode: http.StatusConflict,
		Body:       `{"message":"escrow already settled: escrow 1"}`,
	}
	assert.True(t, transport.IsUpstreamEscrowSettled(settled))
	assert.True(t, transport.IsUpstreamEscrowSettled(fmt.Errorf("send to host: %w", settled)))
	assert.False(t, transport.IsUpstreamEscrowNotFound(settled))

	versionConflict := &transport.UpstreamStatusError{
		Path:       "/sessions/1/chat/completions",
		StatusCode: http.StatusConflict,
		Body:       `{"message":"session version conflict: stored v1, host v2"}`,
	}
	assert.False(t, transport.IsUpstreamEscrowSettled(versionConflict))

	headerOnly := &transport.UpstreamStatusError{
		Path:          "/sessions/1/chat/completions",
		StatusCode:    http.StatusConflict,
		Body:          `{"error":"conflict"}`,
		DevshardError: transport.DevshardErrorEscrowSettled,
	}
	assert.True(t, transport.IsUpstreamEscrowSettled(headerOnly))
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
