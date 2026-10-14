package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
)

func finishedSuspiciousAttempt(nonce uint64) *inflight {
	attempt := unfinishedSuspiciousAttempt(nonce)
	attempt.resp = &host.HostResponse{Mempool: []*types.DevshardTx{{
		Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: nonce}},
	}}}
	return attempt
}

func unfinishedSuspiciousAttempt(nonce uint64) *inflight {
	done := make(chan struct{})
	close(done)
	attempt := &inflight{nonce: nonce, suspicious: true, done: done, resp: &host.HostResponse{}}
	attempt.contentChunks.Store(3)
	return attempt
}

func runningRival(nonce uint64) *inflight {
	return &inflight{nonce: nonce, sendTime: time.Now(), done: make(chan struct{})}
}

// Test flow:
//  1. Hold a finished suspicious answer beside a rival that has no first token, beside a rival that is streaming, and beside a running probe only; then put an unfinished suspicious answer ahead of a finished one.
//  2. Ask whether a suspicious answer is released for each.
//  3. Assert it is released only while every real rival is still without a first token, never for a probe alone, and that the finished answer is chosen over an earlier unfinished one.
func TestASuspiciousAnswerIsReleasedOnlyWhileNoRivalIsStreaming(t *testing.T) {
	streaming := runningRival(3)
	streaming.setFirstTokenAt(time.Now())
	probe := runningRival(4)
	probe.probe = true

	stuckWinner, stuckReleased := suspiciousWinnerRelease([]*inflight{finishedSuspiciousAttempt(1), runningRival(2)})
	_, streamingReleased := suspiciousWinnerRelease([]*inflight{finishedSuspiciousAttempt(1), streaming})
	_, probeOnlyReleased := suspiciousWinnerRelease([]*inflight{finishedSuspiciousAttempt(1), probe})
	laterWinner, laterReleased := suspiciousWinnerRelease([]*inflight{unfinishedSuspiciousAttempt(1), finishedSuspiciousAttempt(5), runningRival(2)})

	require.True(t, stuckReleased)
	require.Equal(t, uint64(1), stuckWinner.nonce)
	require.False(t, streamingReleased)
	require.False(t, probeOnlyReleased)
	require.True(t, laterReleased)
	require.Equal(t, uint64(5), laterWinner.nonce)
}
