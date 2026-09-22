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

func TestIsDockerUnavailablePanic(t *testing.T) {
	cases := []struct {
		rec  any
		want bool
	}{
		{"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", true},
		{"permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock", true},
		{"exec: \"docker\": executable file not found in $PATH", true},
		{"docker: command not found", true},
		{"rootless Docker not supported", true},
		{"dial unix /var/run/docker.sock: connect: no such file or directory", true},
		{"runtime error: invalid memory address or nil pointer dereference", false},
		{errors.New("unexpected testcontainers panic"), false},
	}
	for _, tc := range cases {
		if got := isDockerUnavailablePanic(tc.rec); got != tc.want {
			t.Fatalf("isDockerUnavailablePanic(%v)=%v, want %v", tc.rec, got, tc.want)
		}
	}
}

func TestRunOnce_UnexpectedPanicPropagates(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic to propagate")
		}
		if isDockerUnavailablePanic(rec) {
			t.Fatalf("unexpected docker classification for %v", rec)
		}
	}()
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				if isDockerUnavailablePanic(rec) {
					return
				}
				panic(rec)
			}
		}()
		panic("runtime error: invalid memory address or nil pointer dereference")
	}()
}

func requireCtxErr(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context error", err)
	}
}
