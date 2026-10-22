package inference

import (
	"context"
	"errors"
	"strconv"

	"common/chain"
	commonvalidation "common/validation"

	devshardpkg "devshard"
	"devshard/transport"
)

// FetchChallengePayload fetches a challenged inference's request and body from
// its executor once, with the executor signature and the three hash checks
// replay runs, and classifies the result for the voter. It never calls a
// model and takes no validation credit.
func (v *Validator) FetchChallengePayload(ctx context.Context, req devshardpkg.ValidateRequest) devshardpkg.ChallengeFetch {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)
	epochID := resolveValidationEpoch(v.phase, req.EpochID)
	_, response, err := v.fetchPayloadsFor(ctx, req, inferenceID, epochID)
	fetch := classifyChallengeFetch(ctx, v.phase, epochID, response, err)
	if !v.voteFalseOnFetchFailure {
		// The same switch replay reads: with it off, replay casts no vote on
		// a payload the executor failed to keep, to hand over, or to sign
		// over the body it finished with. The voter does the same: a fetch
		// that would settle a fault here is left unchecked, and one the
		// executor failed is left unchecked at the deadline instead of voted
		// PAYLOAD_UNAVAILABLE.
		fetch.ExecutorSide = false
		switch fetch.Outcome {
		case devshardpkg.ChallengeFetchMismatch, devshardpkg.ChallengeFetchUnavailable:
			fetch = devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchLocal, Cause: fetch.Cause}
		}
	}
	return fetch
}

// FetchChallengePayload passes through to the wrapped engine. A challenge
// fetch is not a replay, so it takes no lease.
func (c *LeaseValidator) FetchChallengePayload(ctx context.Context, req devshardpkg.ValidateRequest) devshardpkg.ChallengeFetch {
	fetcher, ok := c.validator.(devshardpkg.ChallengeFetcher)
	if !ok {
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchLocal, Cause: "validation engine cannot fetch payloads"}
	}
	return fetcher.FetchChallengePayload(ctx, req)
}

// classifyChallengeFetch applies replay's fault rules to one challenge fetch,
// with one difference: a rate limit or an executor that has not attached is
// retried inside the window instead of counting at once, since the whole
// group fetches the same body right after the Finish and an executor restart
// looks exactly like that.
func classifyChallengeFetch(ctx context.Context, phase *chain.Phase, epochID uint64, response []byte, err error) devshardpkg.ChallengeFetch {
	if err == nil {
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchFetched, Response: response}
	}
	cause := truncateCause(err.Error())
	switch {
	case errors.Is(err, errExecutorFetchCut):
		// The caller's deadline ended while the executor had not answered
		// the payload call: kept until the window ends, then the executor's.
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchRetry, ExecutorSide: true, Cause: cause}
	case ctx.Err() != nil, errors.Is(err, errPayloadRPCUnavailable), transport.OutboundPeersReleased():
		// The deadline ended on this host's side of the fetch (its own
		// signer, peer or chain lookup), or the fetch cannot run here.
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchLocal, Cause: cause}
	case errors.Is(err, commonvalidation.ErrHashMismatch), errors.Is(err, errExecutorPayloadSignature):
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchMismatch, Cause: cause}
	case errors.Is(err, commonvalidation.ErrPayloadGone):
		// Same retention rule as executorFaultVerdict.
		if phase != nil && phase.EpochID() > epochID+1 {
			return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchPruned, Cause: cause}
		}
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchUnavailable, Cause: cause}
	case errors.Is(err, commonvalidation.ErrPayloadTooLarge):
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchUnavailable, Cause: cause}
	case errors.Is(err, ErrPayloadRateLimited), errors.Is(err, errExecutorPayloadFault):
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchRetry, ExecutorSide: true, Cause: cause}
	case errors.Is(err, transport.ErrPeerNoAttachDoor):
		// The executor refused Attach on an escrow it is a member of: its
		// answer, kept until the window ends like a connection refused.
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchRetry, ExecutorSide: true, Cause: cause}
	default:
		// This host's own peer, signer or chain lookup.
		return devshardpkg.ChallengeFetch{Outcome: devshardpkg.ChallengeFetchRetry, Cause: cause}
	}
}

var (
	_ devshardpkg.ChallengeFetcher = (*Validator)(nil)
	_ devshardpkg.ChallengeFetcher = (*LeaseValidator)(nil)
)
