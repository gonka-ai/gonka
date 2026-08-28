package user

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/transport"
)

// A lost round used to report "unknown", which only repeated what the outcome already said. These are
// the shapes production actually returns, and each names a different thing to go fix.
func TestClassifyVoteError_NamesWhatTheVerifierAnsweredWith(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		err   error
		class string
	}{
		{
			name:  "host build does not serve the escrow's protocol version",
			err:   &transport.UpstreamStatusError{Path: "/sessions/1/verify-timeout", StatusCode: http.StatusNotFound, Body: `version "v3" not found`},
			class: VoteErrorVersionUnsupported,
		},
		{
			name:  "verifier no longer holds the escrow",
			err:   &transport.UpstreamStatusError{StatusCode: http.StatusInternalServerError, Body: "escrow not found"},
			class: VoteErrorEscrowMissing,
		},
		{
			name:  "verifier never saw the inference confirmed",
			err:   &transport.UpstreamStatusError{StatusCode: http.StatusInternalServerError, Body: `{"message":"inference 508: expected started, got 0"}`},
			class: VoteErrorInferenceMissing,
		},
		{
			name:  "nothing came back at all",
			err:   errors.New("dial tcp: connection refused"),
			class: VoteErrorUnreachable,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.class, classifyVoteError(testCase.err))
		})
	}
}

// The class has to survive the wrapping every caller adds on the way up.
func TestClassifyVoteError_LooksThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("collect timeout votes: %w",
		&transport.UpstreamStatusError{StatusCode: http.StatusNotFound, Body: `version "v3" not found`})

	require.Equal(t, VoteErrorVersionUnsupported, classifyVoteError(wrapped))
}

// One string stands for the round, so it must be the class that failed most.
func TestDominantVoteError_ReportsTheClassThatFailedMost(t *testing.T) {
	require.Equal(t, VoteErrorVersionUnsupported, dominantVoteError(map[string]int{
		VoteErrorVersionUnsupported: 5,
		VoteErrorUnreachable:        2,
	}))
	require.Empty(t, dominantVoteError(map[string]int{}), "a round with no errors names none")
}

// Map order is random; a reason that differed between identical rounds would split one situation
// across two counters.
func TestDominantVoteError_BreaksTiesTheSameWayEveryTime(t *testing.T) {
	tied := map[string]int{VoteErrorVersionUnsupported: 3, VoteErrorUnreachable: 3, VoteErrorEscrowMissing: 3}

	first := dominantVoteError(tied)
	for i := 0; i < 50; i++ {
		require.Equal(t, first, dominantVoteError(tied))
	}
	require.Equal(t, VoteErrorEscrowMissing, first, "alphabetical, so the choice is readable")
}
