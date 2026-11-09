package main

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The client wait of an attempt counts the write in progress: a relay that
// stands in one write for the whole grace must not look idle to the session.
func TestInflightClientWaitCountsTheWriteInProgress(t *testing.T) {
	inf := &inflight{}
	require.Zero(t, inf.clientWait())

	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = inf.clientWrite(func() error { <-release; return nil })
		close(done)
	}()
	require.Eventually(t, func() bool { return inf.clientWait() >= 20*time.Millisecond }, time.Second, time.Millisecond,
		"the write in progress counts")
	close(release)
	<-done

	waited := inf.clientWait()
	require.GreaterOrEqual(t, waited, 20*time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, waited, inf.clientWait(), "outside a write the wait stands")
}

// Completing a write must not briefly lose time that a reader has already
// observed. Each trial has one write, so neither a new write nor an idle period
// can explain a sample below the elapsed lower bound.
func TestInflightClientWaitKeepsElapsedTimeAtWriteCompletion(t *testing.T) {
	old := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(old)
	for trial := 0; trial < 100; trial++ {
		inf := &inflight{}
		release := make(chan struct{})
		releaseWrite := sync.OnceFunc(func() { close(release) })
		t.Cleanup(releaseWrite)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = inf.clientWrite(func() error { <-release; return nil })
		}()
		require.Eventually(t, func() bool { return inf.clientWait() >= time.Millisecond },
			time.Second, time.Microsecond, "the write has already waited at least 1ms")
		releaseWrite()
		lost := time.Duration(-1)
		for {
			waited := inf.clientWait()
			if waited < time.Millisecond {
				lost = waited
			}
			select {
			case <-done:
				require.Negative(t, int64(lost),
					"trial %d: completing the write lost known elapsed time: %s", trial, lost)
				goto nextTrial
			default:
			}
		}
	nextTrial:
	}
}
