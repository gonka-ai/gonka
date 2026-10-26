package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type waitStubBridge struct {
	MainnetBridge
	calls   int
	readyAt int
	err     error
}

func (s *waitStubBridge) GetEscrow(escrowID string) (*EscrowInfo, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.calls < s.readyAt {
		return nil, ErrEscrowNotFound
	}
	return &EscrowInfo{EscrowID: escrowID}, nil
}

func fastEscrowPoll(t *testing.T) {
	t.Helper()
	saved := EscrowReadyPollInterval
	EscrowReadyPollInterval = time.Millisecond
	t.Cleanup(func() { EscrowReadyPollInterval = saved })
}

func TestWaitForEscrowReturnsOnceSlotsAssigned(t *testing.T) {
	fastEscrowPoll(t)
	b := &waitStubBridge{readyAt: 3}
	info, err := WaitForEscrow(context.Background(), b, "7", time.Second)
	require.NoError(t, err)
	require.Equal(t, "7", info.EscrowID)
	require.Equal(t, 3, b.calls)
}

func TestWaitForEscrowGivesUpAfterTimeout(t *testing.T) {
	fastEscrowPoll(t)
	b := &waitStubBridge{readyAt: 1 << 30}
	_, err := WaitForEscrow(context.Background(), b, "7", 20*time.Millisecond)
	require.ErrorIs(t, err, ErrEscrowNotFound)
	require.Greater(t, b.calls, 1)
}

func TestWaitForEscrowZeroTimeoutQueriesOnce(t *testing.T) {
	b := &waitStubBridge{readyAt: 1 << 30}
	_, err := WaitForEscrow(context.Background(), b, "7", 0)
	require.ErrorIs(t, err, ErrEscrowNotFound)
	require.Equal(t, 1, b.calls)
}

func TestWaitForEscrowDoesNotRetryOtherErrors(t *testing.T) {
	b := &waitStubBridge{err: ErrChainUnavailable}
	_, err := WaitForEscrow(context.Background(), b, "7", time.Second)
	require.ErrorIs(t, err, ErrChainUnavailable)
	require.Equal(t, 1, b.calls)
}

func TestWaitForEscrowStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &waitStubBridge{readyAt: 1 << 30}
	_, err := WaitForEscrow(ctx, b, "7", time.Second)
	require.True(t, errors.Is(err, context.Canceled))
}
