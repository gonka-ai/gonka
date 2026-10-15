package main

import (
	"context"
	"errors"
	"testing"
)

// Test flow:
//  1. Fill a two-place vote limit.
//  2. Try a third post under a cancelled context, then release one place and post again.
//  3. Assert the third post waits for a place and gives up with the context, and a released place admits the next post.
func TestATimeoutVotePostWaitsForAFreePlace(t *testing.T) {
	posts := make(chan struct{}, 2)
	releaseFirst, firstErr := acquireTimeoutVotePost(context.Background(), posts)
	_, secondErr := acquireTimeoutVotePost(context.Background(), posts)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, overLimitErr := acquireTimeoutVotePost(cancelled, posts)
	releaseFirst()
	_, afterReleaseErr := acquireTimeoutVotePost(context.Background(), posts)

	if firstErr != nil || secondErr != nil {
		t.Fatalf("posts within the limit = (%v, %v), want both admitted", firstErr, secondErr)
	}
	if !errors.Is(overLimitErr, context.Canceled) {
		t.Fatalf("post over the limit = %v, want it to wait until its context is cancelled", overLimitErr)
	}
	if afterReleaseErr != nil {
		t.Fatalf("post after a release = %v, want it admitted", afterReleaseErr)
	}
}
