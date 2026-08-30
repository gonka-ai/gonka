package user

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"devshard/transport"
)

// Vote failure classes. A timeout that gathered no votes reports which way the verifiers failed,
// because the outcome alone ("vote collection failed") only repeats what the tally already said. The
// set is deliberately small: this string reaches accounting's counter key, where every distinct value
// multiplies the rows a participant carries, so it names classes and never counts or addresses.
const (
	VoteErrorVersionUnsupported = "verifier_version_unsupported"
	VoteErrorEscrowMissing      = "verifier_escrow_missing"
	VoteErrorInferenceMissing   = "verifier_inference_missing"
	VoteErrorUnreachable        = "verifier_unreachable"
	VoteErrorQueueExpired       = "verifier_queue_expired"
	VoteErrorRPCTimeout         = "verifier_rpc_timeout"
)

// errVerifierQueueExpired wraps an acquire wait that never got a slot. Distinct
// from a VerifyTimeout that was sent and then hit a deadline, so classifyVoteError
// can split the two.
var errVerifierQueueExpired = errors.New("verifier queue wait expired")

// errVoteNotSent wraps a round abandoned after the slot was won but before the
// RPC left the gateway. The caller's own deadline can surface here, and a
// deadline the verifier never saw must not read as one it failed to answer.
var errVoteNotSent = errors.New("vote not sent")

// classifyVoteError names a failed vote. Anything that never got far enough to say more counts as
// unreachable, which is what a verifier that answered nothing at all amounts to.
func classifyVoteError(err error) string {
	switch {
	case errors.Is(err, errVerifierQueueExpired):
		return VoteErrorQueueExpired
	case errors.Is(err, errVoteNotSent):
		return VoteErrorUnreachable
	}
	var upstream *transport.UpstreamStatusError
	if errors.As(err, &upstream) {
		return classifyUpstreamStatus(upstream)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return VoteErrorRPCTimeout
	}
	return VoteErrorUnreachable
}

func classifyUpstreamStatus(upstream *transport.UpstreamStatusError) string {
	switch {
	case upstream.StatusCode == http.StatusNotFound && strings.Contains(upstream.Body, "version"):
		return VoteErrorVersionUnsupported
	case strings.Contains(upstream.Body, "escrow not found"):
		return VoteErrorEscrowMissing
	case strings.Contains(upstream.Body, "expected started"),
		strings.Contains(upstream.Body, "expected pending"),
		strings.Contains(upstream.Body, "inference"):
		return VoteErrorInferenceMissing
	}
	return VoteErrorUnreachable
}

// dominantVoteError returns the class that failed most, so one string can stand for the round. Ties
// break alphabetically: map order is random, and a reason that differed between identical rounds
// would split one situation across two counters.
func dominantVoteError(counts map[string]int) string {
	dominant, dominantCount := "", 0
	for class, count := range counts {
		if count > dominantCount || (count == dominantCount && class < dominant) {
			dominant, dominantCount = class, count
		}
	}
	return dominant
}
