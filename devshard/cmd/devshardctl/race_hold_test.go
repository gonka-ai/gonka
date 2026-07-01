package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRaceWriterHoldsEarlyContentForPairwisePreferredAttempt(t *testing.T) {
	savedHold := PairwiseWinnerHold
	PairwiseWinnerHold = 100 * time.Millisecond
	t.Cleanup(func() {
		PairwiseWinnerHold = savedHold
	})

	var buf bytes.Buffer
	race := newRaceGroup(context.Background(), context.Background(), "test-escrow", &buf)
	early := &inflight{
		hostID:       "host-a",
		nonce:        1,
		escrowID:     "test-escrow",
		firstTokenCh: make(chan struct{}),
		done:         make(chan struct{}),
	}
	preferred := &inflight{
		hostID:       "host-b",
		nonce:        2,
		escrowID:     "test-escrow",
		firstTokenCh: make(chan struct{}),
		done:         make(chan struct{}),
	}
	race.addWinnerHoldCandidate(preferred)

	earlyDone := make(chan error, 1)
	go func() {
		_, err := (&raceWriter{group: race, nonce: early.nonce, inf: early}).Write([]byte(`data: {"choices":[{"delta":{"content":"early"}}]}` + "\n\n"))
		earlyDone <- err
	}()

	time.Sleep(10 * time.Millisecond)
	_, err := (&raceWriter{group: race, nonce: preferred.nonce, inf: preferred}).Write([]byte(`data: {"choices":[{"delta":{"content":"preferred"}}]}` + "\n\n"))
	require.NoError(t, err)

	require.NoError(t, <-earlyDone)
	require.Equal(t, uint64(2), race.winnerNonce())
	require.Contains(t, buf.String(), `"content":"preferred"`)
	require.NotContains(t, buf.String(), `"content":"early"`)
}
