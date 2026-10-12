package main

import "context"

const maxConcurrentTimeoutVotePosts = 64

var timeoutVotePosts = make(chan struct{}, maxConcurrentTimeoutVotePosts)

func acquireTimeoutVotePost(ctx context.Context, posts chan struct{}) (release func(), err error) {
	select {
	case posts <- struct{}{}:
		return func() { <-posts }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
