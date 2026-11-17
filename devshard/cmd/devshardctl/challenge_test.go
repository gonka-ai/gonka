package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
)

// Which attempts are marked for a challenge once their Finish applies.
func TestChallengeStreamReason(t *testing.T) {
	complete := &host.HostResponse{ReceivedComplete: true}
	cut := &host.HostResponse{}
	for _, testCase := range []struct {
		name       string
		attempt    *inflight
		gatewayCut bool
		model      string
		want       types.ChallengeReason
		wantMark   bool
	}{
		{name: "cut before [DONE] and devshard_meta", attempt: &inflight{resp: cut, err: errors.New("truncated")},
			want: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, wantMark: true},
		{name: "no stream at all", attempt: &inflight{err: errors.New("connection reset")},
			want: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, wantMark: true},
		{name: "receipt-backed and empty", attempt: &inflight{resp: complete, err: errEmptyStream},
			want: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, wantMark: true},
		{name: "receipt, then cut before any content", attempt: receiptThenCut(0),
			want: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, wantMark: true},
		{name: "receipt and content, then cut", attempt: receiptThenCut(3),
			want: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, wantMark: true},
		{name: "cut by the gateway before any receipt", attempt: &inflight{resp: cut, err: context.Canceled}, gatewayCut: true},
		// A race loser or a stalled winner the gateway cancelled after the
		// receipt: the executor may still sign a Finish for it, so the body is
		// judged like any other cut's.
		{name: "cut by the gateway after the receipt and content", attempt: receiptThenCut(3), gatewayCut: true,
			want: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, wantMark: true},
		{name: "cut by the gateway after the receipt, no content", attempt: receiptThenCut(0), gatewayCut: true,
			want: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, wantMark: true},
		{name: "client gone, stream drained through devshard_meta", attempt: &inflight{resp: complete}},
		{name: "probe", attempt: &inflight{resp: cut, probe: true}},
		{name: "phase-change abort", attempt: &inflight{resp: cut, phaseTransitionAborted: true}},
		// The reasoning model that reports completion tokens for a stream
		// with no content: the gateway does not quarantine the host for it
		// (isModelBurnEmpty), so it does not challenge it either.
		{name: "burn-empty model with completion tokens", attempt: burnEmpty(complete, 7), model: types.KimiK26ModelID},
		{name: "burn-empty model with no completion tokens", attempt: burnEmpty(complete, 0), model: types.KimiK26ModelID,
			want: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, wantMark: true},
		{name: "another model with completion tokens and no content", attempt: burnEmpty(complete, 7), model: "llama",
			want: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, wantMark: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reason, mark := challengeStreamReason(testCase.attempt, testCase.gatewayCut, testCase.model)
			require.Equal(t, testCase.wantMark, mark)
			require.Equal(t, testCase.want, reason)
		})
	}
}

// burnEmpty is a receipt-backed attempt that ended with no content and the
// given completion_tokens in its usage chunk.
func burnEmpty(resp *host.HostResponse, completionTokens int64) *inflight {
	attempt := &inflight{resp: resp, err: errEmptyStream}
	attempt.receiptTimeNano.Store(time.Now().UnixNano())
	attempt.usageComplTokens.Store(completionTokens)
	return attempt
}

// How an attempt's stream is closed on the session once the attempt ends. A
// failed attempt whose stream still ran to devshard_meta hands its hashes to
// the served binding like a successful one: left open, the stream would be
// challenged a minute later as outliving the grace, for a fee per nonce.
func TestAttemptStreamClose(t *testing.T) {
	hashes := [][32]byte{{1}}
	complete := &host.HostResponse{ReceivedComplete: true, ReceivedResponseHashes: hashes}
	for _, testCase := range []struct {
		name       string
		attempt    *inflight
		gatewayCut bool
		want       streamClose
	}{
		{name: "succeeded, complete", attempt: &inflight{resp: complete}, want: streamCloseBind},
		{name: "failed after the stream completed", attempt: &inflight{resp: complete, err: errors.New("process response")}, want: streamCloseBind},
		{name: "failed after the stream completed, client gone", attempt: &inflight{resp: complete, err: context.Canceled}, gatewayCut: true, want: streamCloseBind},
		{name: "cut before [DONE]", attempt: &inflight{resp: &host.HostResponse{ReceivedResponseHashes: hashes}, err: errors.New("truncated")}, want: streamCloseChallenge},
		{name: "complete with no hashes", attempt: &inflight{resp: &host.HostResponse{ReceivedComplete: true}}, want: streamCloseEnded},
		{name: "probe", attempt: &inflight{resp: complete, probe: true}, want: streamCloseEnded},
		{name: "cut by the gateway before any receipt", attempt: &inflight{resp: &host.HostResponse{}, err: context.Canceled}, gatewayCut: true, want: streamCloseEnded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, _ := attemptStreamClose(testCase.attempt, testCase.gatewayCut, "llama")
			require.Equal(t, testCase.want, got)
		})
	}
}

// receiptThenCut is an attempt whose signed receipt arrived and whose stream
// was then cut on the way to the gateway after contentChunks content chunks.
func receiptThenCut(contentChunks int64) *inflight {
	attempt := &inflight{resp: &host.HostResponse{}, err: errors.New("unexpected EOF")}
	attempt.receiptTimeNano.Store(time.Now().UnixNano())
	attempt.contentChunks.Store(contentChunks)
	return attempt
}

// The challenge holds the settle barrier from the moment it is handed over
// until it returns, so the escrow is not settled while votes are collected.
func TestHandleChallengeHoldsTheSettleBarrier(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	var started, finished atomic.Int64
	e := env.proxy.redundancy
	e.onRaceCleanupStart = func() { started.Add(1) }
	e.onRaceCleanupDone = func() { finished.Add(1) }

	e.handleChallenge(42, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	require.Equal(t, int64(1), started.Load(), "the barrier is taken before the goroutine starts")
	require.Eventually(t, func() bool { return finished.Load() == 1 }, 5*time.Second, 10*time.Millisecond)

	e.stopped.Store(true)
	e.handleChallenge(43, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	require.Equal(t, int64(1), started.Load(), "a stopped gateway starts no challenge")
}
