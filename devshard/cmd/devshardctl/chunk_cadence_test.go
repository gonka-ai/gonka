package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// streamAt replays a host's chunk timings without a clock: recordChunkGap takes the gap, and the
// counters it reads are the same ones raceWriter.Write bumps.
func streamAt(inf *inflight, gaps ...time.Duration) {
	start := time.Unix(1_700_000_000, 0)
	inf.outputChunks.Add(1)
	inf.setFirstTokenAt(start)
	inf.lastChunkAt.Store(start.UnixNano())
	for _, gap := range gaps {
		inf.outputChunks.Add(1)
		previousNano := inf.lastChunkAt.Swap(inf.lastChunkAt.Load() + int64(gap))
		inf.recordChunkGap(inf.lastChunkAt.Load() - previousNano)
	}
}

// A host that streams steadily and one that emits a chunk then stops look identical in a chunk
// count; the longest silence is what separates them.
func TestChunkCadence_TellsAStalledHostFromASlowOne(t *testing.T) {
	steady := &inflight{}
	streamAt(steady, 40*time.Millisecond, 40*time.Millisecond, 40*time.Millisecond)

	stalled := &inflight{}
	streamAt(stalled, 40*time.Millisecond, 55*time.Second, 40*time.Millisecond)

	require.Equal(t, 40*time.Millisecond, steady.longestChunkGap())
	require.Equal(t, 55*time.Second, stalled.longestChunkGap())
	require.Equal(t, steady.outputChunks.Load(), stalled.outputChunks.Load(), "the chunk count cannot tell them apart")
}

func TestChunkCadence_NamesTheChunkTheSilenceFollowed(t *testing.T) {
	inf := &inflight{}
	streamAt(inf, 10*time.Millisecond, 30*time.Second, 10*time.Millisecond)

	require.Equal(t, int64(3), inf.maxChunkGapAt.Load())
}

func TestChunkCadence_AveragesTheGapsAcrossTheAttempt(t *testing.T) {
	inf := &inflight{}
	streamAt(inf, 100*time.Millisecond, 200*time.Millisecond, 300*time.Millisecond)

	require.Equal(t, 200*time.Millisecond, inf.meanChunkGap())
}

// Through the real writer: a host whose [DONE] trails the last token has not gone quiet, and
// counting that silence would report a stall on every well-behaved stream.
func TestRaceWriterChunkGap_IgnoresTheSilenceBeforeDone(t *testing.T) {
	silence := 90 * time.Second
	cases := []struct {
		name       string
		lastEvent  string
		wantMeasur bool
	}{
		{"an ordinary chunk after a silence", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", true},
		{"the end-of-stream marker", "data: [DONE]\n\n", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			group := newRaceGroup(ctx, ctx, "escrow-cadence", io.Discard)
			inf := &inflight{
				hostID:       "host-1",
				escrowID:     "escrow-cadence",
				nonce:        1,
				done:         make(chan struct{}),
				receiptCh:    make(chan struct{}),
				firstTokenCh: make(chan struct{}),
			}
			writer := &raceWriter{group: group, nonce: 1, inf: inf}
			_, err := writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"))
			require.NoError(t, err)

			inf.lastChunkAt.Store(time.Now().Add(-silence).UnixNano())
			_, err = writer.Write([]byte(testCase.lastEvent))
			require.NoError(t, err)

			if testCase.wantMeasur {
				require.GreaterOrEqual(t, inf.longestChunkGap(), silence)
				return
			}
			require.Zero(t, inf.longestChunkGap())
		})
	}
}

func TestChunkCadence_ReportsNothingBeforeASecondChunk(t *testing.T) {
	inf := &inflight{}
	streamAt(inf)

	require.Zero(t, inf.longestChunkGap())
	require.Zero(t, inf.meanChunkGap())
}

// A role-only chunk is a token, not content. Stamping first content on it is what made the
// participant metric report a prefill no client ever waited for.
func TestRaceWriterFirstContent_IgnoresARoleOnlyChunk(t *testing.T) {
	ctx := context.Background()
	group := newRaceGroup(ctx, ctx, "escrow-content", io.Discard)
	inf := &inflight{
		hostID:       "host-1",
		escrowID:     "escrow-content",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	writer := &raceWriter{group: group, nonce: 1, inf: inf}

	_, err := writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	require.NoError(t, err)
	require.False(t, inf.firstTokenAt().IsZero(), "a role chunk is still a token")
	require.True(t, inf.firstContentAt().IsZero(), "and it is not content")

	_, err = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	require.NoError(t, err)
	firstContent := inf.firstContentAt()
	require.False(t, firstContent.IsZero())

	_, err = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n"))
	require.NoError(t, err)
	require.Equal(t, firstContent, inf.firstContentAt(), "later content must not move the stamp")
}
