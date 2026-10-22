package main

import (
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
