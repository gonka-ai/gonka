package engine

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

type finishRecordingHandler struct {
	scriptedTimeoutHandler
	claimedFinishTx []byte
}

func (h *finishRecordingHandler) HandleErrorMiss(ctx context.Context, nonce uint64, finishTx, responsePayload []byte) (user.TimeoutResult, error) {
	h.claimedFinishTx = finishTx
	return h.scriptedTimeoutHandler.HandleErrorMiss(ctx, nonce, finishTx, responsePayload)
}

// Test flow:
//  1. Define a table of the session's queued Finish (none, a different one) and the Finish the host's reply carried (one, none).
//  2. For each case, settle a `SettleByMissClaim` step whose proof carries the reply's Finish through a `SessionTimeouts` over a handler queuing the session's.
//  3. Assert the miss is claimed with the reply's Finish whenever the reply carried one and with the session's otherwise, and voted as a timeout when neither has one.
func TestAMissIsClaimedWithTheFinishTheReplyCarriedBeforeTheSessions(t *testing.T) {
	replyFinishTx := []byte("reply-finish")
	queuedFinishTx := []byte("queued-finish")
	cases := []struct {
		name          string
		queued        []byte
		replyFinishTx []byte
		wantClaimed   []byte
		wantVotes     int
	}{
		{name: "the reply's Finish when the session queued none", replyFinishTx: replyFinishTx, wantClaimed: replyFinishTx},
		{name: "the reply's Finish over a different queued one", queued: queuedFinishTx, replyFinishTx: replyFinishTx, wantClaimed: replyFinishTx},
		{name: "the queued Finish when the reply carried none", queued: queuedFinishTx, wantClaimed: queuedFinishTx},
		{name: "a vote when neither has one", wantVotes: 1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler := &finishRecordingHandler{scriptedTimeoutHandler: scriptedTimeoutHandler{finishTx: testCase.queued}}
			poster := &SessionTimeouts{handler: handler}
			step := TimeoutStep{
				Nonce: 7,
				Kind:  SettleByMissClaim,
				Proof: &MissProof{ResponsePayload: []byte(`{"events":[]}`), finishTx: testCase.replyFinishTx},
			}

			if _, err := poster.SettleTimeout(context.Background(), step); err != nil {
				t.Fatalf("SettleTimeout() = %v", err)
			}

			if !bytes.Equal(handler.claimedFinishTx, testCase.wantClaimed) {
				t.Errorf("claimed with Finish %q, want %q", handler.claimedFinishTx, testCase.wantClaimed)
			}
			if handler.calls != testCase.wantVotes {
				t.Errorf("the vote ran %d times, want %d", handler.calls, testCase.wantVotes)
			}
		})
	}
}

// Test flow:
//  1. Run an attempt whose host reply carries a Finish and whose error chunk the classifier proves as a miss.
//  2. Assert the outcome claims a miss and its proof carries the reply's Finish.
func TestAMissProofCarriesTheFinishTheReplyCarried(t *testing.T) {
	replyFinishTx := []byte("reply-finish")
	dispatch := &fakeDispatcher{receipt: true, chunks: []string{"data: chunk\n\n"}, response: heldFinishResponse{fakeResponse: fakeResponse{confirmed: true, finishTx: replyFinishTx}, releases: &atomic.Int32{}}}
	fixture := newAttemptFixture(dispatch, &fakeClassifier{perChunk: []chunkFacts{{Error: true, ErrorSource: "sse", ErrorCode: "500", ErrorType: "server_error", ErrorMessage: "boom"}}})
	fixture.spec.Classifier = provingClassifier{fakeClassifier: fixture.classifier}

	runAttempt(context.Background(), fixture.spec)
	outcome := *doneEvent(t, fixture.drain()).Outcome

	require.True(t, outcome.claimsMiss(), "the fixture must end in an error the attempt can prove")
	require.Equal(t, replyFinishTx, outcome.MissProof.finishTx)
}
