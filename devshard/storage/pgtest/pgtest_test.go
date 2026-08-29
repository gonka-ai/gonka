package pgtest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSleepContext_RespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sleepContext(ctx, time.Second)
	requireCtxErr(t, err)
}

func TestSleepContext_ZeroIsNoop(t *testing.T) {
	if err := sleepContext(context.Background(), 0); err != nil {
		t.Fatalf("sleepContext(0)=%v", err)
	}
}

func TestLockStart_RespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unlock, err := lockStart(ctx)
	if unlock != nil {
		unlock()
		t.Fatal("expected no unlock func when ctx is already done")
	}
	requireCtxErr(t, err)
}

func TestTerminateNilIsNoop(t *testing.T) {
	terminate(nil)
}

func TestErrDockerUnavailableSentinel(t *testing.T) {
	err := errors.Join(ErrDockerUnavailable, errors.New("permission denied"))
	if !errors.Is(err, ErrDockerUnavailable) {
		t.Fatal("expected errors.Is to match ErrDockerUnavailable")
	}
}

func requireCtxErr(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context error", err)
	}
}
