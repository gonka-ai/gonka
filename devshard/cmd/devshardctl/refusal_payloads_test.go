package main

import (
	"context"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

func TestTimeoutVoteUnjudgedIncludesInsufficientVotes(t *testing.T) {
	require.True(t, timeoutVoteUnjudged(user.TimeoutResult{Outcome: string(accounting.TimeoutInsufficientVotes)}))
	require.True(t, timeoutVoteUnjudged(user.TimeoutResult{Outcome: string(accounting.TimeoutVoteCollectionFailed)}))
	require.True(t, timeoutVoteUnjudged(user.TimeoutResult{Outcome: string(accounting.TimeoutDiffSendFailed)}))
	require.False(t, timeoutVoteUnjudged(user.TimeoutResult{Outcome: string(accounting.TimeoutApplied)}))
	require.False(t, timeoutVoteUnjudged(user.TimeoutResult{Outcome: "skipped"}))
}

func TestSoftTimeoutRetryPauseReturnsWhenDeadlineAlreadyOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Nil session: only ctx is checked; deadline path needs a real session.
	require.NoError(t, softTimeoutRetryPause(ctx, nil, 1))

	ctxCanceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	require.ErrorIs(t, softTimeoutRetryPause(ctxCanceled, nil, 1), context.Canceled)
}
