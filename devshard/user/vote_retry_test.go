package user

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/transport"
	"devshard/types"
)

// failFirstVerifier fails its first call with err, then delegates.
type failFirstVerifier struct {
	inner TimeoutVerifier
	err   error
	calls atomic.Int32
}

func (v *failFirstVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff) (bool, []byte, uint32, error) {
	if v.calls.Add(1) == 1 {
		return false, nil, 0, v.err
	}
	return v.inner.VerifyTimeout(ctx, inferenceID, reason, payload, diffs)
}

// A round short of weight because a connection was stale is a round lost to nothing.
func TestAVoteThePeerNeverAnsweredIsAskedAgain(t *testing.T) {
	fixture := newTimeoutVoteFixture(t)
	verifier := &failFirstVerifier{
		inner: &slotClaimingVerifier{signer: fixture.signers[0], claimedSlot: 0, escrowID: "escrow-1"},
		err:   fmt.Errorf("write tcp: %w", net.ErrClosed),
	}

	votes := fixture.collect(t, map[int]TimeoutVerifier{0: verifier})

	require.EqualValues(t, 2, verifier.calls.Load(), "the stale connection must be retried exactly once")
	require.Len(t, votes, 1, "the retry's vote must be counted")
}

// An answered request is the peer's verdict, so repeating it only spends the round's remaining time.
func TestAnAnsweredVoteIsNotAskedAgain(t *testing.T) {
	fixture := newTimeoutVoteFixture(t)
	verifier := &failFirstVerifier{
		inner: &slotClaimingVerifier{signer: fixture.signers[0], claimedSlot: 0, escrowID: "escrow-1"},
		err:   &transport.UpstreamStatusError{StatusCode: http.StatusInternalServerError, Body: "expected started, got 2"},
	}

	votes := fixture.collect(t, map[int]TimeoutVerifier{0: verifier})

	require.EqualValues(t, 1, verifier.calls.Load(), "a verdict from the peer must not be retried")
	require.Empty(t, votes)
}
