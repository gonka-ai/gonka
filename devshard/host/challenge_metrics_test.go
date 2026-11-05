package host

import (
	"context"
	"testing"
	"time"

	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/observability"
	"devshard/types"
)

// A kept verdict is counted once by its fault; asking again returns the kept
// vote without counting it a second time.
func TestVerifyChallenge_CountsKeptVotesByFault(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 5*time.Second)
	f := newChallengeFixture(t)
	fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchMismatch}}}
	h := f.voter(t, 0, fetcher, 60)
	before := promtest.ToFloat64(observability.ChallengeVoteCounter("hash_mismatch"))

	for range 2 {
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH, answer.Fault)
	}
	require.Equal(t, 1.0, promtest.ToFloat64(observability.ChallengeVoteCounter("hash_mismatch"))-before)
}
